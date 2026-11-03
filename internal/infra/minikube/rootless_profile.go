package minikube

import (
	"fmt"
	"os"
	"regexp"

	"k8s.io/minikube/pkg/minikube/config"
)

func configureNodeProfile(cc *config.ClusterConfig, rootless bool, volume string) error {
	if rootless || os.Getenv("CLOUD_ROOTLESS") == "true" {
		cc.KubernetesConfig.ContainerRuntime = "containerd"
		cc.Nodes[0].ContainerRuntime = "containerd"
		cc.KubernetesConfig.FeatureGates = "KubeletInUserNamespace=true"
	}
	if volume != "" {
		if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(volume) {
			return fmt.Errorf("invalid shared storage volume")
		}
		cc.ContainerVolumeMounts = []string{volume + ":/cloud-volumes"}
	}

	return nil
}
