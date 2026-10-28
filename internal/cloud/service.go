package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/storage"
)

// Service serializes infrastructure mutations and owns the durable cloud state.
type Service struct {
	storage       storage.Backend
	storageTotal  int64
	storageServer string
	volumeMu      sync.Mutex
	mu            sync.Mutex
	workMu        sync.Mutex
	state         snapshot
	inf           infra.Infra
	dir           string
	logger        *slog.Logger
	changed       chan struct{}
}

// NewService loads state and reconciles requests interrupted by a previous process.
func NewService(inf infra.Infra, dataDir string, logger *slog.Logger) (*Service, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{inf: inf, dir: dataDir, logger: logger, changed: make(chan struct{}), state: snapshot{Clusters: []Cluster{}, Machines: []Machine{}, Operations: []Operation{}}}
	data, err := os.ReadFile(filepath.Join(dataDir, "state.json")) // #nosec G304 -- state lives in the operator-selected data directory.
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("load cloud state: %w", err)
	}
	if err := s.reconcile(); err != nil {
		return nil, err
	}
	s.retryVolumeDeletions()
	return s, nil
}

func (s *Service) reconcile() error {
	now := time.Now().UTC()
	for i := range s.state.Operations {
		op := &s.state.Operations[i]
		if op.Status == "RUNNING" {
			op.Status = "ERROR"
			op.Error = "interrupted by restart"
			op.EndedAt = &now
		}
	}
	for ci := range s.state.Clusters {
		c := &s.state.Clusters[ci]
		observed, err := s.inf.Machines(context.Background(), c.Name)
		if err != nil {
			c.Status = "ERROR"
			s.logger.Error("cannot reconcile cluster; ownership preserved for cleanup", "cluster", c.Name, "error", err)
			continue
		}
		observedByName := make(map[string]infra.Machine, len(observed))
		for _, m := range observed {
			observedByName[m.Name] = m
		}
		kept := make([]Machine, 0, len(observed))
		for _, m := range s.state.Machines {
			if actual, ok := observedByName[m.Name]; ok {
				m.Status = "CREATING"
				if actual.Running {
					m.Status = "RUNNING"
				}
				kept = append(kept, m)
				delete(observedByName, m.Name)
			} else {
				op := s.state.newOperation("DELETE_MACHINE", m.Name, "cloud")
				entry := s.state.operation(op.ID)
				entry.Status = "ERROR"
				entry.Error = "machine missing during reconciliation: " + m.Name
				entry.EndedAt = &now
			}
		}
		for _, m := range observedByName {
			kept = append(kept, fromInfra(m, *c, "cloud", now))
		}
		s.state.Machines = kept
		if c.Status != "RUNNING" || !slices.ContainsFunc(observed, func(m infra.Machine) bool { return m.ControlPlane && m.Running }) {
			c.Status = "ERROR"
		}
	}
	return save(s.dir, s.state)
}

func (st *snapshot) cluster(name string) (*Cluster, error) {
	for i := range st.Clusters {
		if st.Clusters[i].Name == name {
			return &st.Clusters[i], nil
		}
	}
	return nil, fmt.Errorf("%w: cluster %s", ErrNotFound, name)
}

func (s *Service) ready(name string) (Cluster, error) {
	c, err := s.state.cluster(name)
	if err != nil {
		return Cluster{}, err
	}
	if c.Status != "RUNNING" {
		return Cluster{}, fmt.Errorf("%w: cluster %s is %s", ErrFailedPrecondition, name, c.Status)
	}
	return *c, nil
}

func fromInfra(m infra.Machine, c Cluster, requester string, now time.Time) Machine {
	status := MachineStatus("CREATING")
	if m.Running {
		status = "RUNNING"
	}
	return Machine{Name: m.Name, Cluster: c.Name, MachineType: c.MachineType, ControlPlane: m.ControlPlane, Status: status, CreatedBy: requester, CreatedAt: now}
}

// CreateCluster accepts one cluster and provisions at least its minimum size.
func (s *Service) CreateCluster(name, kind string, minM, maxM int, requester string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.Clusters) > 0 {
		return Operation{}, fmt.Errorf("%w: a cluster already exists: %s", ErrConflict, s.state.Clusters[0].Name)
	}
	shape, err := machineType(kind)
	if err != nil {
		return Operation{}, err
	}
	if len(validation.IsDNS1123Label(name)) > 0 || minM < 1 || maxM < minM || maxM > math.MaxInt32 {
		return Operation{}, fmt.Errorf("%w: require a DNS-1123 name and 1 <= min <= max <= 2147483647", ErrInvalid)
	}
	next := s.state.clone()
	c := Cluster{Name: name, MachineType: kind, MinMachines: minM, MaxMachines: maxM, Status: "PROVISIONING", CreatedAt: time.Now().UTC()}
	next.Clusters = append(next.Clusters, c)
	op := next.newOperation("CREATE_CLUSTER", name, requester)
	next.Machines = append(next.Machines, Machine{Name: name, Cluster: name, MachineType: kind, ControlPlane: true, Status: "CREATING", CreatedBy: requester, CreatedAt: c.CreatedAt})
	if err := s.store(next); err != nil {
		return Operation{}, err
	}
	var observed []infra.Machine
	s.launch(op.ID, func() error {
		if err := s.inf.CreateCluster(context.Background(), infra.ClusterSpec{Name: name, KubernetesVersion: "v1.35.6", Shape: infra.MachineShape{CPUs: shape.CPUs, MemoryMiB: shape.MemoryMiB}, NodeLabels: NodeLabels(kind)}); err != nil {
			return err
		}
		for n := 1; n < minM; n++ {
			if _, err := s.inf.AddMachine(context.Background(), name); err != nil {
				return err
			}
		}
		var err error
		observed, err = s.inf.Machines(context.Background(), name)
		return err
	}, func(st *snapshot, err error) {
		cluster, _ := st.cluster(name)
		cluster.Status = "ERROR"
		if err == nil {
			cluster.Status = "RUNNING"
			st.Machines = nil
			for _, m := range observed {
				st.Machines = append(st.Machines, fromInfra(m, c, requester, c.CreatedAt))
			}
		}
	})
	return op, nil
}

// DeleteCluster accepts teardown of a stable cluster, including a failed creation.
func (s *Service) DeleteCluster(name, requester string) (Operation, error) {
	return s.DeleteClusterIfUnchanged(name, requester, nil)
}

// DeleteClusterIfUnchanged atomically checks the selected generation before accepting teardown.
func (s *Service) DeleteClusterIfUnchanged(name, requester string, expected *time.Time) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.state.cluster(name)
	if err != nil {
		return Operation{}, err
	}
	if expected != nil && !current.CreatedAt.Equal(*expected) {
		return Operation{}, fmt.Errorf("%w: cluster changed; refresh before deleting", ErrFailedPrecondition)
	}
	for _, op := range s.state.Operations {
		if op.Status == "RUNNING" {
			return Operation{}, fmt.Errorf("%w: an operation is still running", ErrFailedPrecondition)
		}
	}
	next := s.state.clone()
	c, _ := next.cluster(name)
	c.Status = "DELETING"
	op := next.newOperation("DELETE_CLUSTER", name, requester)
	for i := range next.Machines {
		next.Machines[i].Status = "DELETING"
	}
	if err := s.store(next); err != nil {
		return Operation{}, err
	}
	s.launch(op.ID, func() error {
		if err := s.inf.DeleteCluster(context.Background(), name); err != nil {
			return err
		}
		for _, volume := range s.Volumes() {
			if !volume.Static && volume.Cluster == name && volume.Status == "Bound" {
				if err := s.releaseVolumeFor(volume.ID, volume.Cluster, volume.Claim, volume.ClaimUID, "Retain"); err != nil {
					return err
				}
			}
		}

		return s.clearClusterPublications(name)
	}, func(st *snapshot, err error) {
		if err == nil {
			st.Clusters = []Cluster{}
			st.Machines = []Machine{}
		} else {
			c, _ := st.cluster(name)
			c.Status = "ERROR"
		}
	})
	return op, nil
}

// Clusters returns the cloud's managed clusters.
func (s *Service) Clusters() []Cluster {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Cluster{}, s.state.Clusters...)
}

// Cluster returns a named cluster.
func (s *Service) Cluster(name string) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.state.cluster(name)
	if err != nil {
		return Cluster{}, err
	}
	return *c, nil
}

// Machines lists machines, optionally filtered by cluster.
func (s *Service) Machines(name string) ([]Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "" {
		if _, err := s.state.cluster(name); err != nil {
			return nil, err
		}
	}
	result := []Machine{}
	for _, m := range s.state.Machines {
		if name == "" || m.Cluster == name {
			result = append(result, m)
		}
	}
	return result, nil
}

// AddMachines reserves capacity before queuing asynchronous worker creation.
func (s *Service) AddMachines(name string, n int, requester string) ([]Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.ready(name)
	if err != nil {
		return nil, err
	}
	target := 0
	for _, m := range s.state.Machines {
		if m.Status != "DELETING" {
			target++
		}
	}
	if n <= 0 {
		return nil, fmt.Errorf("%w: increase must be positive", ErrInvalid)
	}
	if n > c.MaxMachines-target {
		return nil, fmt.Errorf("%w: maximum size is %d", ErrFailedPrecondition, c.MaxMachines)
	}
	next := s.state.clone()
	ops := make([]Operation, 0, n)
	for range n {
		op := next.newOperation("ADD_MACHINE", name, requester)
		op.Target = name + "-pending-" + op.ID
		next.operation(op.ID).Target = op.Target
		ops = append(ops, op)
		next.Machines = append(next.Machines, Machine{Name: op.Target, Cluster: name, MachineType: c.MachineType, Status: "CREATING", CreatedBy: requester, CreatedAt: op.StartedAt})
	}
	if err := s.store(next); err != nil {
		return nil, err
	}
	for _, op := range ops {
		var added infra.Machine
		var observed []infra.Machine
		var observationErr error
		s.launch(op.ID, func() error {
			var err error
			added, err = s.inf.AddMachine(context.Background(), name)
			if err != nil {
				observed, observationErr = s.inf.Machines(context.Background(), name)
				return errors.Join(err, observationErr)
			}
			return nil
		}, func(st *snapshot, err error) {
			if err != nil {
				// An unobservable failure must keep its capacity reservation.
				if observationErr != nil {
					if added.Name != "" {
						st.recordAddedMachines(op, c, []infra.Machine{added})
					}
					return
				}
				st.recordAddedMachines(op, c, observed)
				return
			}
			st.recordAddedMachines(op, c, []infra.Machine{added})
		})
	}
	return ops, nil
}

func (st *snapshot) recordAddedMachines(op Operation, c Cluster, observed []infra.Machine) {
	st.Machines = slices.DeleteFunc(st.Machines, func(m Machine) bool { return m.Name == op.Target })
	for _, actual := range observed {
		if !slices.ContainsFunc(st.Machines, func(m Machine) bool { return m.Name == actual.Name }) {
			st.Machines = append(st.Machines, fromInfra(actual, c, op.Requester, op.StartedAt))
			st.operation(op.ID).Target = actual.Name
		}
	}
}

// DeleteMachine protects the control plane and the minimum ready capacity.
func (s *Service) DeleteMachine(name, machine, requester string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.ready(name)
	if err != nil {
		return Operation{}, err
	}
	index := -1
	running := 0
	for i, m := range s.state.Machines {
		if m.Status == "RUNNING" {
			running++
		}
		if m.Name == machine {
			index = i
		}
	}
	if index < 0 {
		return Operation{}, fmt.Errorf("%w: machine %s", ErrNotFound, machine)
	}
	m := s.state.Machines[index]
	if m.ControlPlane || m.Status != "RUNNING" || running <= c.MinMachines {
		return Operation{}, fmt.Errorf("%w: control plane, minimum size, or machine state prevents deletion", ErrFailedPrecondition)
	}
	next := s.state.clone()
	next.Machines[index].Status = "DELETING"
	op := next.newOperation("DELETE_MACHINE", machine, requester)
	if err := s.store(next); err != nil {
		return Operation{}, err
	}
	s.launch(op.ID, func() error { return s.inf.DeleteMachine(context.Background(), name, machine) }, func(st *snapshot, err error) {
		if err == nil {
			st.Machines = slices.DeleteFunc(st.Machines, func(m Machine) bool { return m.Name == machine })
		} else {
			for i := range st.Machines {
				if st.Machines[i].Name == machine {
					st.Machines[i].Status = "RUNNING"
				}
			}
		}
	})
	return op, nil
}

// Kubeconfig exports credentials for a ready cluster.
func (s *Service) Kubeconfig(name string) ([]byte, error) {
	s.mu.Lock()
	_, err := s.ready(name)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.inf.Kubeconfig(context.Background(), name)
}
