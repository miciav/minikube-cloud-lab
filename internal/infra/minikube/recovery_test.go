package minikube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/minikube/pkg/drivers/kic"
	"k8s.io/minikube/pkg/libmachine/host"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/machine"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func isolatedBackend(t *testing.T) (*Infra, string) {
	t.Helper()
	t.Setenv("MINIKUBE_HOME", os.Getenv("MINIKUBE_HOME"))
	t.Setenv("KUBECONFIG", os.Getenv("KUBECONFIG"))
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b, dir
}

func TestRestartBeforeProfileExists(t *testing.T) {
	b, dir := isolatedBackend(t)
	data, err := json.Marshal(map[string]any{"clusters": []cloud.Cluster{{Name: "demo", MachineType: "lab-small", MinMachines: 1, MaxMachines: 3, Status: "PROVISIONING"}}, "machines": []cloud.Machine{{Name: "demo", Cluster: "demo", Status: "CREATING"}}, "operations": []cloud.Operation{{ID: "op-000001", Status: "RUNNING", StartedAt: time.Now()}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cloud.NewService(b, dir, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("daemon cannot restart or accept delete: %v", err)
	}
}

func TestDeleteWithUnavailableDockerMustFail(t *testing.T) {
	for _, mode := range []string{"unavailable", "ignored-removal"} {
		t.Run(mode, func(t *testing.T) {
			b, _ := isolatedBackend(t)
			cc := &config.ClusterConfig{Name: "demo", Driver: "docker", Nodes: []config.Node{{ControlPlane: true}}}
			if err := config.SaveProfile("demo", cc); err != nil {
				t.Fatal(err)
			}
			api, err := machine.NewAPIClient(options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = api.Close() }()
			driver := kic.NewDriver(kic.Config{MachineName: "demo", ClusterName: "demo", OCIBinary: "docker"})
			if err := api.Save(&host.Host{Name: "demo", DriverName: "docker", Driver: driver}); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			if mode == "ignored-removal" {
				if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\ncase \"$1\" in\nps) printf 'demo\\n';;\ncontainer) printf 'broken\\n';;\nesac\n"), 0o700); err != nil { // #nosec G306 -- executable fake Docker fixture, not credentials.
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			err = b.DeleteCluster(context.Background(), "demo")
			if err == nil {
				t.Fatalf("delete reported success without Docker; profile still present=%v", config.ProfileExists("demo"))
			}
			if !config.ProfileExists("demo") {
				t.Fatal("failed deletion discarded recovery profile")
			}
		})
	}
}

func TestMachineObservationDoesNotRequireHostRecords(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			b, _ := isolatedBackend(t)
			if err := config.SaveProfile("demo", &config.ClusterConfig{Name: "demo", Driver: "docker", Nodes: []config.Node{{ControlPlane: true}}}); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			output := ""
			if present {
				output = "demo"
			}
			script := "#!/bin/sh\ncase \"$1\" in\nps) printf '" + output + "\\n';;\ncontainer) printf 'true\\n';;\nesac\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700); err != nil { // #nosec G306 -- executable fake Docker test fixture.
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			machines, err := b.Machines(context.Background(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if present {
				want = 1
			}
			if len(machines) != want {
				t.Fatalf("observed %v want %d", machines, want)
			}
			if present && (!machines[0].Running || !machines[0].ControlPlane) {
				t.Fatalf("lost actual machine state: %v", machines)
			}
		})
	}
}
