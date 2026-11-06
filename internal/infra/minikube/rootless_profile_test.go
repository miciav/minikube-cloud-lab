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

func TestRootlessDriverHasExplicitPIDLimit(t *testing.T) {
	for _, rootless := range []bool{false, true} {
		cc := config.ClusterConfig{Name: "pid-check", Driver: "docker", CPUs: 2, Memory: 2048, Nodes: []config.Node{{Port: 8443}}}
		if rootless {
			cc.KubernetesConfig.FeatureGates = "KubeletInUserNamespace=true"
		}
		for _, name := range []string{"", "m02"} {
			node := cc.Nodes[0]
			node.Name = name
			d, err := nodeDriverConfig(cc, node)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, arg := range d.NodeConfig.ExtraArgs {
				if arg == "--pids-limit=4096" {
					found = true
				}
			}
			if found != rootless {
				t.Fatalf("rootless=%v node=%s args=%v", rootless, name, d.NodeConfig.ExtraArgs)
			}
		}
	}
}
