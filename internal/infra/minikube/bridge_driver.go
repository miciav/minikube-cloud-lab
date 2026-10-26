package minikube

import (
	"encoding/json"

	"k8s.io/minikube/pkg/libmachine/drivers"
)

// Inside the shared bridge, SSH reaches the node directly instead of a host-loopback mapping.
type privateSSHDriver struct{ drivers.Driver }

func (d privateSSHDriver) GetSSHHostname() (string, error) { return d.GetIP() }
func (privateSSHDriver) GetSSHPort() (int, error)          { return 22, nil }

func (d privateSSHDriver) MarshalJSON() ([]byte, error) { return json.Marshal(d.Driver) }
