package minikube

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/minikube/pkg/minikube/config"
)

func TestFailedProvisioningForgetsOnlyAbsentWorker(t *testing.T) {
	for _, tc := range []struct {
		name, ps string
		want     int
		err      bool
	}{{"absent", "demo", 1, false}, {"created", "demo\ndemo-m02", 2, false}, {"docker-down", "", 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			_, _ = isolatedBackend(t)
			cc := &config.ClusterConfig{Name: "demo", Driver: "docker", Nodes: []config.Node{{ControlPlane: true}, {Name: "m02", Worker: true}}}
			if err := config.SaveProfile("demo", cc); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			script := "#!/bin/sh\nprintf '" + tc.ps + "\\n'\n"
			if tc.err {
				script = "#!/bin/sh\nexit 1\n"
			}
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700); err != nil { // #nosec G306 -- executable fake Docker fixture.
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			err := forgetUnprovisioned(nil, "demo", "m02", "demo-m02")
			if (err != nil) != tc.err {
				t.Fatalf("error %v", err)
			}
			p, err := config.LoadProfile("demo")
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Config.Nodes) != tc.want {
				t.Fatalf("nodes %d, want %d", len(p.Config.Nodes), tc.want)
			}
		})
	}
}

func TestNextWorkerKeepsClusterKubernetesVersion(t *testing.T) {
	cc := &config.ClusterConfig{
		KubernetesConfig: config.KubernetesConfig{KubernetesVersion: "v1.35.6", ContainerRuntime: "docker"},
		Nodes:            []config.Node{{ControlPlane: true}, {Name: "m02", Worker: true}},
	}
	n := nextWorker(cc)
	if n.Name != "m03" || n.KubernetesVersion != "v1.35.6" || n.ContainerRuntime != "docker" || !n.Worker || n.ControlPlane {
		t.Fatalf("worker %+v", n)
	}
}
