package minikube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/minikube/pkg/drivers/kic"
	"k8s.io/minikube/pkg/libmachine/host"
	"k8s.io/minikube/pkg/minikube/command"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/machine"
)

type bridgeTestDriver struct{ *kic.Driver }

func (bridgeTestDriver) GetIP() (string, error) { return "172.30.0.2", nil }

func TestBridgeSSHUsesPrivateNodeAndPreservesDriverRecord(t *testing.T) {
	_, _ = isolatedBackend(t)
	base := bridgeTestDriver{kic.NewDriver(kic.Config{MachineName: "demo", ClusterName: "demo", Network: "minikube-cloud-lab", OCIBinary: "docker"})}
	d := privateSSHDriver{Driver: base}
	ip, err := d.GetSSHHostname()
	if err != nil || ip != "172.30.0.2" {
		t.Fatalf("SSH must use private node IP: %s %v", ip, err)
	}
	port, err := d.GetSSHPort()
	if err != nil || port != 22 {
		t.Fatalf("SSH must use private node port: %d %v", port, err)
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var original kic.Driver
	if err := json.Unmarshal(data, &original); err != nil || original.NodeConfig.Network != "minikube-cloud-lab" || original.MachineName != "demo" {
		t.Fatalf("driver JSON lost its fields: %s, %v", data, err)
	}
	api, err := machine.NewAPIClient(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = api.Close() }()
	if err := api.Save(&host.Host{Name: "demo", DriverName: "docker", Driver: &d}); err != nil {
		t.Fatal(err)
	}
	loaded, err := api.Load("demo")
	if err != nil || loaded.Driver.GetMachineName() != "demo" {
		t.Fatalf("saved driver cannot be reloaded: %v", err)
	}
}

func TestVerifyNodesUsesPrivateContextAndChecksPressure(t *testing.T) {
	for _, pressure := range []bool{false, true} {
		t.Run(fmt.Sprint(pressure), func(t *testing.T) {
			var reached atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/nodes" {
					t.Errorf("unexpected health request %s", r.URL.Path)
				}
				reached.Store(true)
				condition := "False"
				if pressure {
					condition = "True"
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"kind":"NodeList","apiVersion":"v1","items":[{"metadata":{"name":"demo"},"status":{"conditions":[{"type":"MemoryPressure","status":%q}]}}]}`, condition)
			}))
			defer server.Close()
			yakdConfig(t, server.URL)
			err := verifyNodes(t.Context(), "demo", command.NewFakeCommandRunner())
			if !reached.Load() || (err != nil) != pressure {
				t.Fatalf("private health endpoint reached=%v pressure=%v error=%v", reached.Load(), pressure, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := verifyNodes(ctx, "demo", command.NewFakeCommandRunner()); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled health check: %v", err)
			}
		})
	}
}

func TestBridgeWorkerUsesPrivateManagedCredentials(t *testing.T) {
	b, _ := isolatedBackend(t)
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/demo-m02" {
			t.Errorf("unexpected private request %s", r.URL.Path)
		} else {
			reached.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","message":"private API reached","reason":"ServiceUnavailable","code":503}`))
	}))
	defer server.Close()
	cfg := clientapi.NewConfig()
	cfg.Clusters["demo"] = &clientapi.Cluster{Server: server.URL}
	cfg.Contexts["demo"] = &clientapi.Context{Cluster: "demo"}
	cfg.Clusters["unrelated"] = &clientapi.Cluster{Server: "http://127.0.0.1:1"}
	cfg.Contexts["unrelated"] = &clientapi.Context{Cluster: "unrelated"}
	cfg.CurrentContext = "unrelated"
	if err := clientcmd.WriteToFile(*cfg, filepath.Join(b.home, "kubeconfig")); err != nil {
		t.Fatal(err)
	}
	public := cfg.DeepCopy()
	public.CurrentContext = "demo"
	public.Clusters["demo"].Server = "http://127.0.0.1:1"
	if err := clientcmd.WriteToFile(*public, filepath.Join(b.home, "demo.kubeconfig")); err != nil {
		t.Fatal(err)
	}
	cc := &config.ClusterConfig{Name: "demo", Nodes: []config.Node{{ControlPlane: true}, {Name: "m02", Worker: true}}}
	err := b.removeWorker(t.Context(), cc, "demo-m02")
	if err == nil || !reached.Load() {
		t.Fatalf("worker drain did not use managed private endpoint: %v", err)
	}
}

func TestBridgeConfigurationValidation(t *testing.T) {
	for _, tc := range []struct {
		name, network, ip string
		valid             bool
	}{
		{"legacy", "", "", true},
		{"bridge", "minikube-cloud-lab", "172.30.0.254", true},
		{"network-only", "minikube-cloud-lab", "", false},
		{"ip-only", "", "172.30.0.254", false},
		{"invalid-ip", "minikube-cloud-lab", "wrong", false},
		{"public-ip", "minikube-cloud-lab", "8.8.8.254", false},
		{"node-collision", "minikube-cloud-lab", "172.30.0.2", false},
		{"ipv6", "minikube-cloud-lab", "fd00::fe", false},
		{"invalid-network", "wrong name", "172.30.0.254", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MINIKUBE_HOME", os.Getenv("MINIKUBE_HOME"))
			t.Setenv("KUBECONFIG", os.Getenv("KUBECONFIG"))
			t.Setenv("CLOUD_DOCKER_NETWORK", tc.network)
			t.Setenv("CLOUD_DOCKER_IP", tc.ip)
			_, err := New(t.TempDir())
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestBridgeClusterSettingsUsePrivateEndpoint(t *testing.T) {
	_, _ = isolatedBackend(t)
	cc := &config.ClusterConfig{Name: "demo", Driver: "docker", KeepContext: true, KubernetesConfig: config.KubernetesConfig{APIServerName: "minikubeCA"}}
	n := &config.Node{IP: "172.30.0.2", Port: 8443, ControlPlane: true}
	settings, err := clusterSettings(cc, n, &host.Host{DriverName: "docker"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ClusterServerAddress != "https://172.30.0.2:8443" || settings.KeepContext || !cc.KeepContext {
		t.Fatalf("private endpoint/context: %+v", settings)
	}
}

func TestBridgeProviderAddressDoesNotInspectHostGateway(t *testing.T) {
	ip, err := providerAddress(&host.Host{DriverName: "unavailable-gateway"}, "demo", "172.30.0.254")
	if err != nil || ip.String() != "172.30.0.254" {
		t.Fatalf("explicit provider address must not need a host gateway: %v, %v", ip, err)
	}
}

func TestForwardedAPIServerRequiresLoopbackMapping(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"loopback", `{"NetworkSettings":{"Ports":{"8443/tcp":[{"HostIp":"127.0.0.1","HostPort":"32443"}]}}}`, "https://127.0.0.1:32443", 200},
		{"missing", `{"NetworkSettings":{"Ports":{}}}`, "", 200},
		{"public-binding", `{"NetworkSettings":{"Ports":{"8443/tcp":[{"HostIp":"0.0.0.0","HostPort":"32443"}]}}}`, "", 200},
		{"invalid-port", `{"NetworkSettings":{"Ports":{"8443/tcp":[{"HostIp":"127.0.0.1","HostPort":"wrong"}]}}}`, "", 200},
		{"out-of-range", `{"NetworkSettings":{"Ports":{"8443/tcp":[{"HostIp":"127.0.0.1","HostPort":"65536"}]}}}`, "", 200},
		{"daemon-error", `{"message":"inspect unavailable"}`, "", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/_ping" {
					w.Header().Set("API-Version", "1.51")
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/containers/demo/json") || r.Method != http.MethodGet {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			t.Setenv("DOCKER_HOST", strings.Replace(server.URL, "http://", "tcp://", 1))
			t.Setenv("DOCKER_TLS_VERIFY", "")
			t.Setenv("DOCKER_CERT_PATH", "")
			t.Setenv("DOCKER_API_VERSION", "")
			got, err := forwardedAPIServer(t.Context(), "demo")
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unsafe/missing mapping accepted: %s", got)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("host endpoint=%s want=%s error=%v", got, tc.want, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := forwardedAPIServer(ctx, "demo"); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation swallowed: %v", err)
			}
		})
	}
}
