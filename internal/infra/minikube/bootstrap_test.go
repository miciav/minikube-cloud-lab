package minikube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"k8s.io/minikube/pkg/drivers/kic"
	"k8s.io/minikube/pkg/libmachine/host"
	"k8s.io/minikube/pkg/minikube/command"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/machine"
)

func TestDeleteFailureKeepsDaemonAlive(t *testing.T) {
	if os.Getenv("CLOUD_REVIEW_FATAL_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestDeleteFailureKeepsDaemonAlive$") // #nosec G204 -- launches only this test binary to detect upstream os.Exit.
		cmd.Env = append(os.Environ(), "CLOUD_REVIEW_FATAL_CHILD=1")
		if err := cmd.Run(); err != nil {
			t.Fatalf("DeleteMachine terminated the caller instead of returning an operation error: %v", err)
		}
		return
	}
	b, _ := isolatedBackend(t)
	cc := &config.ClusterConfig{Name: "demo", Driver: "docker", Nodes: []config.Node{{ControlPlane: true}, {Name: "m02", Worker: true}}}
	if err := config.SaveProfile("demo", cc); err != nil {
		t.Fatal(err)
	}
	api, err := machine.NewAPIClient(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = api.Close() }()
	for _, name := range []string{"demo", "demo-m02"} {
		driver := kic.NewDriver(kic.Config{MachineName: name, ClusterName: "demo", OCIBinary: "docker"})
		if err := api.Save(&host.Host{Name: name, DriverName: "docker", Driver: driver}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if err := b.DeleteMachine(context.Background(), "demo", "demo-m02"); err == nil {
		t.Fatal("missing infrastructure must return an error")
	}
}

type failedStartupRunner struct{ *command.FakeCommandRunner }

func (failedStartupRunner) RunCmd(*exec.Cmd) (*command.RunResult, error) {
	return &command.RunResult{}, errors.New("startup transport unavailable")
}

func TestStartupFailureReturnsError(t *testing.T) {
	_, err := configureRuntime(context.Background(), config.ClusterConfig{KubernetesConfig: config.KubernetesConfig{KubernetesVersion: "v1.35.6", ContainerRuntime: "docker", NetworkPlugin: "cni"}}, failedStartupRunner{command.NewFakeCommandRunner()})
	if err == nil || !strings.Contains(err.Error(), "startup transport unavailable") {
		t.Fatalf("startup failure not returned: %v", err)
	}
}

func TestClusterDNSUsesManagedContext(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			core := ".:53 {\n errors\n forward . /etc/resolv.conf\n}"
			if existing {
				core = ".:53 {\n hosts {\n 10.0.0.7 existing.local\n fallthrough\n }\n forward . /etc/resolv.conf\n}"
			}
			updates := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/namespaces/kube-system/configmaps/coredns" {
					http.NotFound(w, r)
					return
				}
				cm := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system", ResourceVersion: "1"}, Data: map[string]string{"Corefile": core}}
				if r.Method == http.MethodPut {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if _, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, cm); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					updates <- cm.Data["Corefile"]
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(cm)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "config")
			cfg := clientapi.NewConfig()
			cfg.Clusters["demo"] = &clientapi.Cluster{Server: server.URL}
			cfg.Contexts["demo"] = &clientapi.Context{Cluster: "demo"}
			if err := clientcmd.WriteToFile(*cfg, path); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KUBECONFIG", path)
			if err := configureClusterDNS(context.Background(), "demo", "10.0.0.1"); err != nil {
				t.Fatal(err)
			}
			updated := <-updates
			if !strings.Contains(updated, "10.0.0.1 host.minikube.internal") || strings.Count(updated, "hosts {") != 1 || !strings.Contains(updated, "forward . /etc/resolv.conf") {
				t.Fatalf("invalid DNS update: %q", updated)
			}
			if existing && !strings.Contains(updated, "existing.local") {
				t.Fatal("existing DNS record lost")
			}
		})
	}
}
