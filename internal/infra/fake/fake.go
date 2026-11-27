// Package fake provides in-memory infrastructure for service tests.
package fake

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

// Call records a request made at the infrastructure boundary.
type Call struct {
	Method, Cluster, Machine string
	Spec                     infra.ClusterSpec
	Instance                 infra.InstanceSpec
	Image                    infra.ImageSpec
	Registry                 infra.RegistrySpec
}

// Infra records requests and supports errors and delays configured before use.
type Infra struct {
	mu        sync.Mutex
	machines  map[string][]infra.Machine
	instances map[string]infra.Instance
	images    map[string]bool
	// registries are containers; registryData are data volumes and their images.
	registries   map[string]infra.RegistryInfo
	registryData map[string][]infra.RegistryImage
	calls        []Call
	next         int
	created      int
	Errors       map[string]error
	Delay        time.Duration
	// ImageCloudInit is reported by every capture.
	ImageCloudInit bool
}

// New returns an empty fake backend.
func New() *Infra {
	return &Infra{machines: make(map[string][]infra.Machine), instances: make(map[string]infra.Instance), images: make(map[string]bool), registries: make(map[string]infra.RegistryInfo), registryData: make(map[string][]infra.RegistryImage), Errors: make(map[string]error)}
}

func (f *Infra) record(ctx context.Context, c Call) error {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	err := f.Errors[c.Method]
	delay := f.Delay
	f.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

// Calls returns a snapshot of the recorded requests.
func (f *Infra) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CreateCluster creates a single control plane.
func (f *Infra) CreateCluster(ctx context.Context, spec infra.ClusterSpec) error {
	if err := f.record(ctx, Call{Method: "CreateCluster", Cluster: spec.Name, Spec: spec}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.machines[spec.Name] = []infra.Machine{{Name: spec.Name, ControlPlane: true, Running: true}}
	return nil
}

// DeleteCluster removes a cluster and its machines.
func (f *Infra) DeleteCluster(ctx context.Context, name string) error {
	if err := f.record(ctx, Call{Method: "DeleteCluster", Cluster: name}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.machines, name)
	return nil
}

// AddMachine adds a worker.
func (f *Infra) AddMachine(ctx context.Context, name string) (infra.Machine, error) {
	if err := f.record(ctx, Call{Method: "AddMachine", Cluster: name}); err != nil {
		return infra.Machine{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	m := infra.Machine{Name: fmt.Sprintf("%s-m%02d", name, f.next+1), Running: true}
	f.machines[name] = append(f.machines[name], m)
	return m, nil
}

// DeleteMachine removes the named machine.
func (f *Infra) DeleteMachine(ctx context.Context, cluster, name string) error {
	if err := f.record(ctx, Call{Method: "DeleteMachine", Cluster: cluster, Machine: name}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, m := range f.machines[cluster] {
		if m.Name == name {
			f.machines[cluster] = append(f.machines[cluster][:i], f.machines[cluster][i+1:]...)
			break
		}
	}
	return nil
}

// Machines returns the observed machines.
func (f *Infra) Machines(ctx context.Context, name string) ([]infra.Machine, error) {
	if err := f.record(ctx, Call{Method: "Machines", Cluster: name}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]infra.Machine{}, f.machines[name]...), nil
}

// Kubeconfig returns parseable credentials with embedded certificate data.
func (f *Infra) Kubeconfig(ctx context.Context, name string) ([]byte, error) {
	if err := f.record(ctx, Call{Method: "Kubeconfig", Cluster: name}); err != nil {
		return nil, err
	}
	return fmt.Appendf(nil, `apiVersion: v1
kind: Config
clusters:
- name: %s
  cluster:
    server: https://127.0.0.1:6443
    certificate-authority-data: Y2E=
contexts:
- name: %s
  context:
    cluster: %s
    user: %s
current-context: %s
users:
- name: %s
  user:
    client-certificate-data: Y2VydA==
    client-key-data: a2V5
`, name, name, name, name, name, name), nil
}

// RunServiceExposure waits for daemon shutdown without background work.
func (f *Infra) RunServiceExposure(ctx context.Context) error { <-ctx.Done(); return nil }

// CreateInstance starts an instance at the next fake address.
func (f *Infra) CreateInstance(ctx context.Context, spec infra.InstanceSpec) (infra.Instance, error) {
	if err := f.record(ctx, Call{Method: "CreateInstance", Instance: spec}); err != nil {
		return infra.Instance{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := infra.Instance{Name: spec.Name, IP: fmt.Sprintf("172.30.0.%d", 200+f.created), HostKey: "ssh-ed25519 FAKE-" + spec.Name, Running: true, GroupID: spec.GroupID}
	f.created++
	f.instances[spec.Name] = i
	return i, nil
}

// StartInstance runs an existing instance.
func (f *Infra) StartInstance(ctx context.Context, name string) (infra.Instance, error) {
	if err := f.record(ctx, Call{Method: "StartInstance", Instance: infra.InstanceSpec{Name: name}}); err != nil {
		return infra.Instance{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.instances[name]
	if !ok {
		return infra.Instance{}, fmt.Errorf("instance %s not found", name)
	}
	i.Running = true
	f.instances[name] = i
	return i, nil
}

// StopInstance stops an existing instance.
func (f *Infra) StopInstance(ctx context.Context, name string) error {
	if err := f.record(ctx, Call{Method: "StopInstance", Instance: infra.InstanceSpec{Name: name}}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.instances[name]
	if !ok {
		return fmt.Errorf("instance %s not found", name)
	}
	i.Running = false
	f.instances[name] = i
	return nil
}

// DeleteInstance removes an instance; an absent one is already deleted.
func (f *Infra) DeleteInstance(ctx context.Context, name string) error {
	if err := f.record(ctx, Call{Method: "DeleteInstance", Instance: infra.InstanceSpec{Name: name}}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.instances, name)
	return nil
}

// DeleteOwnedInstance removes only the requested group's instance.
func (f *Infra) DeleteOwnedInstance(ctx context.Context, name, groupID string) error {
	if err := f.record(ctx, Call{Method: "DeleteInstance", Instance: infra.InstanceSpec{Name: name, GroupID: groupID}}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if current, ok := f.instances[name]; ok && current.GroupID != groupID {
		return fmt.Errorf("instance %s ownership mismatch", name)
	}
	delete(f.instances, name)
	return nil
}

// InstanceLoadBalancerRunning reports a live frontend by default.
func (f *Infra) InstanceLoadBalancerRunning(ctx context.Context) (bool, error) {
	return true, f.record(ctx, Call{Method: "InstanceLoadBalancerRunning"})
}

// Instances returns the observed instances sorted by name.
func (f *Infra) Instances(ctx context.Context) ([]infra.Instance, error) {
	if err := f.record(ctx, Call{Method: "Instances"}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]infra.Instance, 0, len(f.instances))
	for _, i := range f.instances {
		out = append(out, i)
	}
	slices.SortFunc(out, func(a, b infra.Instance) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// SetDashboard records the requested addon action for service lifecycle tests.
func (f *Infra) SetDashboard(ctx context.Context, name, _ string, enabled bool) error {
	method := "UninstallDashboard"
	if enabled {
		method = "InstallDashboard"
	}
	return f.record(ctx, Call{Method: method, Cluster: name})
}

// CaptureImage stores the image name and reports a fixed size.
func (f *Infra) CaptureImage(ctx context.Context, spec infra.ImageSpec) (infra.CapturedImage, error) {
	if err := f.record(ctx, Call{Method: "CaptureImage", Image: spec}); err != nil {
		return infra.CapturedImage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[spec.Name] = true
	return infra.CapturedImage{Bytes: 1 << 20, CloudInit: f.ImageCloudInit}, nil
}

// DeleteImage removes an image; an absent one is already deleted.
func (f *Infra) DeleteImage(ctx context.Context, name string) error {
	if err := f.record(ctx, Call{Method: "DeleteImage", Image: infra.ImageSpec{Name: name}}); err != nil {
		return err
	}
	f.ForgetImage(name)
	return nil
}

// ImageExists reports whether a capture stored the image.
func (f *Infra) ImageExists(ctx context.Context, name string) (bool, error) {
	if err := f.record(ctx, Call{Method: "ImageExists", Image: infra.ImageSpec{Name: name}}); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[name], nil
}

// ForgetImage drops an image without a call, as a runtime prune would.
func (f *Infra) ForgetImage(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.images, name)
}

// CreateRegistry runs a registry container on its existing or new data.
func (f *Infra) CreateRegistry(ctx context.Context, spec infra.RegistrySpec) (infra.RegistryInfo, error) {
	if err := f.record(ctx, Call{Method: "CreateRegistry", Registry: spec}); err != nil {
		return infra.RegistryInfo{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := infra.RegistryInfo{Name: spec.Name, IP: fmt.Sprintf("172.30.0.%d", 200+f.created), Running: true}
	f.created++
	f.registries[spec.Name] = r
	if _, ok := f.registryData[spec.Name]; !ok {
		f.registryData[spec.Name] = nil // new, empty data
	}
	return r, nil
}

// DeleteRegistry removes a registry container and its data.
func (f *Infra) DeleteRegistry(ctx context.Context, name string) error {
	if err := f.record(ctx, Call{Method: "DeleteRegistry", Registry: infra.RegistrySpec{Name: name}}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.registries, name)
	delete(f.registryData, name)
	return nil
}

// StopRegistries removes every registry container and keeps the data.
func (f *Infra) StopRegistries(ctx context.Context) error {
	if err := f.record(ctx, Call{Method: "StopRegistries"}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	clear(f.registries)
	return nil
}

// Registries returns the registry containers sorted by name.
func (f *Infra) Registries(ctx context.Context) ([]infra.RegistryInfo, error) {
	if err := f.record(ctx, Call{Method: "Registries"}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]infra.RegistryInfo, 0, len(f.registries))
	for _, r := range f.registries {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b infra.RegistryInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// RegistryImages returns the images stored in a registry's data.
func (f *Infra) RegistryImages(ctx context.Context, spec infra.RegistrySpec) ([]infra.RegistryImage, error) {
	if err := f.record(ctx, Call{Method: "RegistryImages", Registry: spec}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.registryData[spec.Name]), nil
}

// RegistryBytes sums the image sizes in a registry's data.
func (f *Infra) RegistryBytes(ctx context.Context, name string) (int64, error) {
	if err := f.record(ctx, Call{Method: "RegistryBytes", Registry: infra.RegistrySpec{Name: name}}); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, image := range f.registryData[name] {
		total += image.Bytes
	}
	return total, nil
}

// SetRegistryImages replaces a registry's stored images, as pushes would.
func (f *Infra) SetRegistryImages(name string, images ...infra.RegistryImage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registryData[name] = images
}

// RegistryData reports whether a registry's data exists.
func (f *Infra) RegistryData(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.registryData[name]
	return ok
}
