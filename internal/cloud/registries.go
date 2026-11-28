package cloud

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

// Registry is a managed container registry. It never carries credentials.
type Registry struct {
	Name      string    `json:"name"`
	Endpoint  string    `json:"endpoint"`
	Status    string    `json:"status"`
	IP        string    `json:"ip,omitempty"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// RegistryCredential is one account of a registry.
type RegistryCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Endpoint string `json:"endpoint"`
}

// registryRecord is the persisted registry with its secrets.
type registryRecord struct {
	Registry
	PushPassword string `json:"pushPassword"`
	PullPassword string `json:"pullPassword"`
}

func (r registryRecord) spec() infra.RegistrySpec {
	return infra.RegistrySpec{Name: r.Name, PushUser: registryPushUser, PushPassword: r.PushPassword, PullUser: registryPullUser, PullPassword: r.PullPassword}
}

// MaxRegistries bounds registries on the lab network.
const MaxRegistries = 3

// RegistryPort is the HTTP port of every registry endpoint.
const RegistryPort = 5000

const (
	registryPushUser = "push"
	registryPullUser = "pull"
	errNoRegistries  = unavailable("registries are not supported by this backend")
)

func registryEndpoint(name string) string { return fmt.Sprintf("%s.registry:%d", name, RegistryPort) }

func newRegistryPassword() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b) // never fails since Go 1.24
	return base64.RawURLEncoding.EncodeToString(b)
}

func (st *snapshot) registry(name string) (*registryRecord, error) {
	for i := range st.Registries {
		if st.Registries[i].Name == name {
			return &st.Registries[i], nil
		}
	}
	return nil, fmt.Errorf("%w: registry %s", ErrNotFound, name)
}

func (st *snapshot) registryBusy(name string) bool {
	return slices.ContainsFunc(st.Operations, func(op Operation) bool {
		return op.Status == "RUNNING" && op.Target == name && strings.HasSuffix(op.Type, "_REGISTRY")
	})
}

// CreateRegistry accepts one registry with fresh push and pull credentials.
func (s *Service) CreateRegistry(name, requester string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registries == nil {
		return Operation{}, errNoRegistries
	}
	if len(validation.IsDNS1123Label(name)) > 0 {
		return Operation{}, fmt.Errorf("%w: registry name must be a DNS-1123 label", ErrInvalid)
	}
	if _, err := s.state.registry(name); err == nil {
		return Operation{}, fmt.Errorf("%w: registry %s already exists", ErrConflict, name)
	}
	if len(s.state.Registries) >= MaxRegistries {
		return Operation{}, fmt.Errorf("%w: at most %d registries", ErrConflict, MaxRegistries)
	}
	record := registryRecord{Registry: Registry{Name: name, Status: "CREATING", CreatedBy: requester, CreatedAt: time.Now().UTC()}, PushPassword: newRegistryPassword(), PullPassword: newRegistryPassword()}
	next := s.state.clone()
	next.Registries = append(next.Registries, record)
	op := next.newOperation("CREATE_REGISTRY", name, requester)
	if err := s.store(next); err != nil {
		return Operation{}, err
	}
	var observed infra.RegistryInfo
	s.launch(op.ID, func() error {
		var err error
		observed, err = s.registries.CreateRegistry(context.Background(), record.spec())
		return err
	}, func(st *snapshot, err error) { finishRegistry(st, name, observed, err) })
	return op, nil
}

func finishRegistry(st *snapshot, name string, observed infra.RegistryInfo, err error) {
	r, lookup := st.registry(name)
	if lookup != nil {
		return
	}
	r.Status, r.IP = "ERROR", observed.IP // a failed create reports no address
	if err == nil {
		r.Status = "RUNNING"
	}
}

// registryListTimeout bounds the image listing a non-forced delete waits for.
const registryListTimeout = 10 * time.Second

// deletableRegistry returns the spec of a registry no operation is working on.
func (st *snapshot) deletableRegistry(name string) (infra.RegistrySpec, error) {
	current, err := st.registry(name)
	if err != nil {
		return infra.RegistrySpec{}, err
	}
	if st.registryBusy(name) {
		return infra.RegistrySpec{}, fmt.Errorf("%w: an operation is running on registry %s", ErrFailedPrecondition, name)
	}
	return current.spec(), nil
}

// DeleteRegistry removes a registry and its data; one holding images needs force.
func (s *Service) DeleteRegistry(name string, force bool, requester string) (Operation, error) {
	if s.registries == nil {
		return Operation{}, errNoRegistries
	}
	if !force {
		// The listing is an HTTP call to the registry: readers must not wait for it.
		s.mu.Lock()
		spec, err := s.state.deletableRegistry(name)
		s.mu.Unlock()
		if err != nil {
			return Operation{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), registryListTimeout)
		images, err := s.registries.RegistryImages(ctx, spec)
		cancel()
		if err != nil {
			return Operation{}, fmt.Errorf("%w: cannot list images of registry %s (%w); delete with force", ErrFailedPrecondition, name, err)
		}
		if len(images) > 0 {
			return Operation{}, fmt.Errorf("%w: registry %s holds %d images; delete with force", ErrFailedPrecondition, name, len(images))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.state.deletableRegistry(name); err != nil {
		return Operation{}, err
	}
	next := s.state.clone()
	r, _ := next.registry(name)
	r.Status = "DELETING"
	op := next.newOperation("DELETE_REGISTRY", name, requester)
	if err := s.store(next); err != nil {
		return Operation{}, err
	}
	s.launch(op.ID, func() error {
		return s.registries.DeleteRegistry(context.Background(), name)
	}, func(st *snapshot, err error) {
		if err == nil {
			st.Registries = slices.DeleteFunc(st.Registries, func(r registryRecord) bool { return r.Name == name })
		} else if r, lookup := st.registry(name); lookup == nil {
			r.Status = "ERROR"
		}
	})
	return op, nil
}

// Registries returns the registries sorted by name, without credentials.
func (s *Service) Registries() []Registry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Registry, 0, len(s.state.Registries))
	for _, r := range s.state.Registries {
		view := r.Registry
		view.Endpoint = registryEndpoint(r.Name)
		out = append(out, view)
	}
	slices.SortFunc(out, func(a, b Registry) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// RegistryCredentials returns the push (read/write) or pull (read-only) account.
func (s *Service) RegistryCredentials(name string, push bool) (RegistryCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.state.registry(name)
	if err != nil {
		return RegistryCredential{}, err
	}
	if push {
		return RegistryCredential{Username: registryPushUser, Password: r.PushPassword, Endpoint: registryEndpoint(name)}, nil
	}
	return RegistryCredential{Username: registryPullUser, Password: r.PullPassword, Endpoint: registryEndpoint(name)}, nil
}

// RegistryImages lists the images stored in a registry.
func (s *Service) RegistryImages(ctx context.Context, name string) ([]infra.RegistryImage, error) {
	if s.registries == nil {
		return nil, errNoRegistries
	}
	s.mu.Lock()
	r, err := s.state.registry(name)
	var spec infra.RegistrySpec
	if err == nil {
		spec = r.spec()
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.registries.RegistryImages(ctx, spec)
}

// StartRegistries removes every registry container, keeping the data, and recreates the recorded ones.
// A registry without a record keeps its data until a registry of the same name is created again.
func (s *Service) StartRegistries(ctx context.Context) error {
	if s.registries == nil {
		return nil
	}
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if err := s.registries.StopRegistries(ctx); err != nil {
		return fmt.Errorf("remove registry containers: %w", err)
	}
	s.mu.Lock()
	records := slices.Clone(s.state.Registries)
	s.mu.Unlock()
	var errs []error
	type outcome struct {
		info    infra.RegistryInfo
		deleted bool
		err     error
	}
	outcomes := make(map[string]outcome, len(records))
	for _, r := range records {
		if r.Status == "DELETING" { // a delete accepted before a crash
			err := s.registries.DeleteRegistry(ctx, r.Name)
			if err != nil {
				errs = append(errs, fmt.Errorf("finish deleting registry %s: %w", r.Name, err))
			}
			outcomes[r.Name] = outcome{deleted: err == nil, err: err}
			continue
		}
		info, err := s.registries.CreateRegistry(ctx, r.spec())
		if err != nil {
			errs = append(errs, fmt.Errorf("start registry %s: %w", r.Name, err))
		}
		outcomes[r.Name] = outcome{info: info, err: err}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state.clone()
	for name, o := range outcomes {
		finishRegistry(&next, name, o.info, o.err)
	}
	next.Registries = slices.DeleteFunc(next.Registries, func(r registryRecord) bool { return outcomes[r.Name].deleted })
	if err := s.store(next); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// StopRegistries removes the registry containers and keeps their records and data.
// It waits for a running operation at most half of ctx's remaining time, then sweeps anyway
// so the containers never outlive the cloud; startup removes or recreates any a late operation leaves.
func (s *Service) StopRegistries(ctx context.Context) error {
	if s.registries == nil {
		return nil
	}
	lockCtx, cancel := ctx, context.CancelFunc(func() {})
	if deadline, ok := ctx.Deadline(); ok {
		lockCtx, cancel = context.WithDeadline(ctx, time.Now().Add(time.Until(deadline)/2))
	}
	defer cancel()
	if s.lockWork(lockCtx) == nil {
		defer s.workMu.Unlock()
	}
	return s.registries.StopRegistries(ctx)
}
