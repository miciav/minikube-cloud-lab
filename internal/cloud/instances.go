package cloud

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/miciav/minikube-cloud-lab/internal/atomicfile"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

// InstanceStatus describes a standalone instance lifecycle.
type InstanceStatus string

// Image is a catalog entry for standalone instances.
type Image struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SSHUser     string `json:"sshUser"`
}

// Instance is a standalone Linux machine outside the cluster.
type Instance struct {
	Name        string         `json:"name"`
	Image       string         `json:"image"`
	MachineType string         `json:"machineType"`
	Status      InstanceStatus `json:"status"`
	IP          string         `json:"ip,omitempty"`
	SSHUser     string         `json:"sshUser"`
	HostKey     string         `json:"hostKey,omitempty"`
	CreatedBy   string         `json:"createdBy"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// MaxInstances bounds standalone instances on the lab network.
// ponytail: fixed lab ceiling; raise it (and the .200–.252 range) if hosts outgrow it.
const MaxInstances = 8

var errNoInstances = fmt.Errorf("%w: standalone instances are not supported by this backend", ErrUnavailable)

// Images returns the fixed instance image catalog.
func Images() []Image {
	return []Image{{Name: "debian-12", Description: "Debian 12 (minikube kicbase) with Docker Engine", SSHUser: "docker"}}
}

// LookupImage returns a catalog entry by name.
func LookupImage(name string) (Image, error) {
	for _, image := range Images() {
		if image.Name == name {
			return image, nil
		}
	}
	return Image{}, fmt.Errorf("%w: unknown image %s", ErrInvalid, name)
}

func (st *snapshot) instance(name string) (*Instance, error) {
	for i := range st.Instances {
		if st.Instances[i].Name == name {
			return &st.Instances[i], nil
		}
	}
	return nil, fmt.Errorf("%w: instance %s", ErrNotFound, name)
}

// instanceBusy ignores cluster operations, whose targets may share the name.
func (st *snapshot) instanceBusy(name string) bool {
	return slices.ContainsFunc(st.Operations, func(op Operation) bool {
		return op.Status == "RUNNING" && op.Target == name && strings.HasSuffix(op.Type, "_INSTANCE")
	})
}

func (s *Service) instanceDir(name string) string { return filepath.Join(s.dir, "instances", name) }

func newInstanceKey() (private []byte, authorized string, err error) {
	public, secret, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	block, err := ssh.MarshalPrivateKey(secret, "")
	if err != nil {
		return nil, "", err
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(block), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), nil
}

// CreateInstance accepts one standalone instance and provisions it asynchronously.
func (s *Service) CreateInstance(name, image, kind, requester string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instances == nil {
		return Operation{}, errNoInstances
	}
	if len(validation.IsDNS1123Label(name)) > 0 {
		return Operation{}, fmt.Errorf("%w: instance name must be a DNS-1123 label", ErrInvalid)
	}
	img, err := LookupImage(image)
	if err != nil {
		return Operation{}, err
	}
	shape, err := LookupMachineType(kind)
	if err != nil {
		return Operation{}, err
	}
	if _, err := s.state.instance(name); err == nil {
		return Operation{}, fmt.Errorf("%w: instance %s already exists", ErrConflict, name)
	}
	if len(s.state.Instances) >= MaxInstances {
		return Operation{}, fmt.Errorf("%w: at most %d instances", ErrConflict, MaxInstances)
	}
	private, authorized, err := newInstanceKey()
	if err != nil {
		return Operation{}, err
	}
	dir := s.instanceDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Operation{}, err
	}
	if err := atomicfile.Write(filepath.Join(dir, "id_ed25519"), private); err != nil {
		return Operation{}, err
	}
	next := s.state.clone()
	next.Instances = append(next.Instances, Instance{Name: name, Image: image, MachineType: kind, Status: "CREATING", SSHUser: img.SSHUser, CreatedBy: requester, CreatedAt: time.Now().UTC()})
	op := next.newOperation("CREATE_INSTANCE", name, requester)
	if err := s.store(next); err != nil {
		_ = os.RemoveAll(dir)
		return Operation{}, err
	}
	spec := infra.InstanceSpec{Name: name, Image: image, Shape: infra.MachineShape{CPUs: shape.CPUs, MemoryMiB: shape.MemoryMiB}, SSHUser: img.SSHUser, AuthorizedKey: authorized}
	var observed infra.Instance
	s.launch(op.ID, func() error {
		var err error
		observed, err = s.instances.CreateInstance(context.Background(), spec)
		return err
	}, func(st *snapshot, err error) {
		s.finishInstance(st, name, observed, err)
	})
	return op, nil
}

// finishInstance records the outcome of an operation that leaves the instance running.
func (s *Service) finishInstance(st *snapshot, name string, observed infra.Instance, err error) {
	i, lookup := st.instance(name)
	if lookup != nil {
		return
	}
	i.Status = "ERROR"
	if err == nil {
		i.Status, i.IP, i.HostKey = "RUNNING", observed.IP, observed.HostKey
	}
}

// Instances returns the standalone instances sorted by name.
func (s *Service) Instances() []Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.state.Instances)
	slices.SortFunc(out, func(a, b Instance) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Instance returns a named standalone instance.
func (s *Service) Instance(name string) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.state.instance(name)
	if err != nil {
		return Instance{}, err
	}
	return *i, nil
}

// instanceOperation validates a lifecycle request and records it before its work starts.
func (s *Service) instanceOperation(name, kind, requester string, check func(Instance) error, during InstanceStatus, work func() error, finish func(*snapshot, error)) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instances == nil {
		return Operation{}, errNoInstances
	}
	current, err := s.state.instance(name)
	if err != nil {
		return Operation{}, err
	}
	if s.state.instanceBusy(name) {
		return Operation{}, fmt.Errorf("%w: an operation is running on instance %s", ErrFailedPrecondition, name)
	}
	if err := check(*current); err != nil {
		return Operation{}, err
	}
	next := s.state.clone()
	if during != "" {
		i, _ := next.instance(name)
		i.Status = during
	}
	op := next.newOperation(kind, name, requester)
	if err := s.store(next); err != nil {
		return Operation{}, err
	}
	s.launch(op.ID, work, finish)
	return op, nil
}

func requireStatus(allowed ...InstanceStatus) func(Instance) error {
	return func(i Instance) error {
		if !slices.Contains(allowed, i.Status) {
			return fmt.Errorf("%w: instance %s is %s", ErrFailedPrecondition, i.Name, i.Status)
		}
		return nil
	}
}

// StartInstance boots a stopped instance, or one whose last operation failed.
func (s *Service) StartInstance(name, requester string) (Operation, error) {
	var observed infra.Instance
	check := func(i Instance) error {
		if i.Status == "ERROR" && i.HostKey == "" {
			return fmt.Errorf("%w: instance %s never finished creating; delete and recreate it", ErrFailedPrecondition, name)
		}
		return requireStatus("STOPPED", "ERROR")(i)
	}
	return s.instanceOperation(name, "START_INSTANCE", requester, check, "", func() error {
		var err error
		observed, err = s.instances.StartInstance(context.Background(), name)
		return err
	}, func(st *snapshot, err error) { s.finishInstance(st, name, observed, err) })
}

// StopInstance halts a running instance and keeps its disk.
func (s *Service) StopInstance(name, requester string) (Operation, error) {
	return s.instanceOperation(name, "STOP_INSTANCE", requester, requireStatus("RUNNING"), "", func() error {
		return s.instances.StopInstance(context.Background(), name)
	}, func(st *snapshot, err error) {
		if i, lookup := st.instance(name); lookup == nil {
			i.Status = "ERROR"
			if err == nil {
				i.Status = "STOPPED"
			}
		}
	})
}

// DeleteInstance removes an instance, its disk and its SSH key.
func (s *Service) DeleteInstance(name, requester string) (Operation, error) {
	return s.instanceOperation(name, "DELETE_INSTANCE", requester, requireStatus("RUNNING", "STOPPED", "ERROR"), "DELETING", func() error {
		if err := s.instances.DeleteInstance(context.Background(), name); err != nil {
			return err
		}
		return os.RemoveAll(s.instanceDir(name))
	}, func(st *snapshot, err error) {
		if err == nil {
			st.Instances = slices.DeleteFunc(st.Instances, func(i Instance) bool { return i.Name == name })
		} else if i, lookup := st.instance(name); lookup == nil {
			i.Status = "ERROR"
		}
	})
}
