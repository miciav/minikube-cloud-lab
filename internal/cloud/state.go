// Package cloud owns clusters, machines, and the durable operation registry.
package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// ClusterStatus describes the cluster lifecycle.
type ClusterStatus string

// MachineStatus describes an instance lifecycle.
type MachineStatus string

// OpStatus describes an asynchronous operation.
type OpStatus string

// MachineType defines an available machine shape.
type MachineType struct {
	Name      string `json:"name"`
	CPUs      int    `json:"cpus"`
	MemoryMiB int    `json:"memoryMiB"`
}

// Cluster is the student's managed Kubernetes cluster.
type Cluster struct {
	Name        string        `json:"name"`
	MachineType string        `json:"machineType"`
	MinMachines int           `json:"minMachines"`
	MaxMachines int           `json:"maxMachines"`
	Status      ClusterStatus `json:"status"`
	CreatedAt   time.Time     `json:"createdAt"`
}

// Machine records a machine and its origin.
type Machine struct {
	Name         string        `json:"name"`
	Cluster      string        `json:"cluster"`
	MachineType  string        `json:"machineType"`
	ControlPlane bool          `json:"controlPlane"`
	Status       MachineStatus `json:"status"`
	CreatedBy    string        `json:"createdBy"`
	CreatedAt    time.Time     `json:"createdAt"`
}

// Operation records an asynchronous request and its outcome.
type Operation struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Target    string     `json:"target"`
	Requester string     `json:"requester"`
	Status    OpStatus   `json:"status"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

// Domain errors are mapped by each transport.
var (
	ErrConflict           = errors.New("conflict")
	ErrNotFound           = errors.New("not found")
	ErrFailedPrecondition = errors.New("failed precondition")
	ErrInvalid            = errors.New("invalid request")
)

// MachineTypes returns the fixed cloud catalog.
func MachineTypes() []MachineType {
	return []MachineType{{Name: "lab-small", CPUs: 2, MemoryMiB: 2048}, {Name: "lab-medium", CPUs: 4, MemoryMiB: 8192}}
}

// NodeLabels returns the standard labels applied during machine bootstrap.
func NodeLabels(kind string) map[string]string {
	return map[string]string{"node.kubernetes.io/instance-type": kind, "topology.kubernetes.io/region": "lab-region-1", "topology.kubernetes.io/zone": "lab-region-1-a"}
}

func machineType(name string) (MachineType, error) {
	for _, kind := range MachineTypes() {
		if kind.Name == name {
			return kind, nil
		}
	}
	return MachineType{}, fmt.Errorf("%w: unknown machine type %s", ErrInvalid, name)
}

// Volume records a persistent cloud storage allocation.
type Volume struct {
	ID       string `json:"id"`
	Cluster  string `json:"cluster"`
	Claim    string `json:"claim"`
	ClaimUID string `json:"claimUID"`
	Capacity string `json:"capacity"`
	Policy   string `json:"policy"`
	Status   string `json:"status"`
}

type snapshot struct {
	Volumes    []Volume    `json:"volumes,omitempty"`
	Clusters   []Cluster   `json:"clusters"`
	Machines   []Machine   `json:"machines"`
	Operations []Operation `json:"operations"`
}

func (st snapshot) clone() snapshot {
	return snapshot{Volumes: slices.Clone(st.Volumes), Clusters: slices.Clone(st.Clusters), Machines: slices.Clone(st.Machines), Operations: slices.Clone(st.Operations)}
}

func save(dir string, st snapshot) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "state.json"))
}
