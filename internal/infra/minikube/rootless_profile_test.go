package minikube

import (
	"testing"

	"k8s.io/minikube/pkg/minikube/config"
)

func TestNodeProfileRetainsRuntimeAndSharedMountForWorkers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		engine       bool
		env, runtime string
	}{{"engine", true, "", "containerd"}, {"configured", false, "true", "containerd"}, {"docker", false, "", "docker"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLOUD_ROOTLESS", tc.env)
			cc := &config.ClusterConfig{Nodes: []config.Node{{ContainerRuntime: "docker"}}, KubernetesConfig: config.KubernetesConfig{ContainerRuntime: "docker"}}
			if err := configureNodeProfile(cc, tc.engine, "student-storage"); err != nil {
				t.Fatal(err)
			}
			if cc.KubernetesConfig.ContainerRuntime != tc.runtime || cc.Nodes[0].ContainerRuntime != tc.runtime {
				t.Fatalf("runtime mismatch: %+v", cc)
			}
			if len(cc.ContainerVolumeMounts) != 1 || cc.ContainerVolumeMounts[0] != "student-storage:/cloud-volumes" {
				t.Fatalf("mount not retained in profile: %v", cc.ContainerVolumeMounts)
			}
			if tc.runtime == "containerd" && cc.KubernetesConfig.FeatureGates != "KubeletInUserNamespace=true" {
				t.Fatal("missing user namespace configuration")
			}
			if err := configureNodeProfile(cc, tc.engine, "/host/path"); err == nil {
				t.Fatal("accepted host path instead of named volume")
			}
		})
	}
}
