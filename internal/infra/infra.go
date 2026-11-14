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

// InstanceSpec describes a standalone instance requested from the backend.
type InstanceSpec struct {
	Name, Image   string // Image is a catalog name such as "debian-12".
	Shape         MachineShape
	SSHUser       string
	AuthorizedKey string // One authorized_keys line.
}

// Instance is the observed state of a standalone instance.
type Instance struct {
	Name, IP, HostKey string
	Running           bool
}

// Instances manages standalone instances, separately from clusters.
type Instances interface {
	CreateInstance(context.Context, InstanceSpec) (Instance, error)
	StartInstance(context.Context, string) (Instance, error)
	StopInstance(context.Context, string) error
	DeleteInstance(context.Context, string) error // An absent instance is success.
	Instances(context.Context) ([]Instance, error)
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
