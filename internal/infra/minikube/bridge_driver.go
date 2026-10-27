package minikube

import (
	"encoding/json"
	"fmt"

	"k8s.io/minikube/pkg/drivers/kic/oci"
	"k8s.io/minikube/pkg/libmachine/drivers"
)

// Inside the shared bridge, SSH reaches the node directly instead of a host-loopback mapping.
type privateSSHDriver struct{ drivers.Driver }

func (d privateSSHDriver) GetSSHHostname() (string, error) { return d.GetIP() }
func (privateSSHDriver) GetSSHPort() (int, error)          { return 22, nil }

func (d privateSSHDriver) MarshalJSON() ([]byte, error) { return json.Marshal(d.Driver) }

func checkBridgeNetwork(name string) error {
	if name == "" {
		return nil
	}
	gateway, err := oci.CreateNetwork("docker", name, "", "")
	if err != nil {
		return fmt.Errorf("prepare bridge network %s: %w", name, err)
	}
	if gateway == nil {
		return fmt.Errorf("bridge network %s has no usable minikube gateway", name)
	}
	return nil
}
