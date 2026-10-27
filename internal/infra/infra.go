// Package infra defines the cloud's infrastructure boundary.
package infra

import "context"

type (
	// MachineShape specifies the resources of each machine.
	MachineShape struct{ CPUs, MemoryMiB int }
	// ClusterSpec describes a cluster requested from the backend.
	ClusterSpec struct {
		Name, KubernetesVersion string
		Shape                   MachineShape
		NodeLabels              map[string]string
	}
)

// Machine is the observed state of an infrastructure instance.
type Machine struct {
	Name                  string
	ControlPlane, Running bool
}

// Infra manages cluster and machine lifecycles.
type Infra interface {
	RunServiceExposure(context.Context) error
	CreateCluster(context.Context, ClusterSpec) error
	DeleteCluster(context.Context, string) error
	AddMachine(context.Context, string) (Machine, error)
	DeleteMachine(context.Context, string, string) error
	Machines(context.Context, string) ([]Machine, error)
	Kubeconfig(context.Context, string) ([]byte, error)
}
