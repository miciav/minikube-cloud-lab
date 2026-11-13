package minikube

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"k8s.io/minikube/pkg/minikube/config"
)

const userNamespaceGate = "KubeletInUserNamespace=true"

// rootless reports whether kubelet runs in a user namespace (rootless Docker or Podman).
func rootless(k config.KubernetesConfig) bool {
	return strings.Contains(k.FeatureGates, userNamespaceGate)
}

func configureNodeProfile(cc *config.ClusterConfig, hostRootless bool, volume string) error {
	if hostRootless || os.Getenv("CLOUD_ROOTLESS") == "true" {
		cc.KubernetesConfig.ContainerRuntime = "containerd"
		cc.Nodes[0].ContainerRuntime = "containerd"
		cc.KubernetesConfig.FeatureGates = userNamespaceGate
	}
	if volume != "" {
		if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(volume) {
			return fmt.Errorf("invalid shared storage volume")
		}
		cc.ContainerVolumeMounts = []string{volume + ":/cloud-volumes"}
	}

	return nil
}
