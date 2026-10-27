package minikube

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestExposureUsesPrivateManagedContext(t *testing.T) {
	b, _ := isolatedBackend(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"ServiceList","items":[]}`))
	}))
	defer server.Close()
	cfg := clientapi.NewConfig()
	cfg.Clusters["demo"] = &clientapi.Cluster{Server: server.URL}
	cfg.Contexts["demo"] = &clientapi.Context{Cluster: "demo"}
	cfg.Contexts["unrelated"] = &clientapi.Context{Cluster: "missing"}
	cfg.CurrentContext = "unrelated"
	if err := clientcmd.WriteToFile(*cfg, filepath.Join(b.home, "kubeconfig")); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		client, err := b.exposureClient("demo")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.CoreV1().Services("").List(t.Context(), metav1.ListOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	unchanged, err := clientcmd.LoadFromFile(filepath.Join(b.home, "kubeconfig"))
	if err != nil || unchanged.CurrentContext != "unrelated" || requests != 2 {
		t.Fatalf("context changed: %+v %v requests%d", unchanged, err, requests)
	}
}

func TestDeleteExposureWithoutProfile(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[broken], func(t *testing.T) {
			b, _ := isolatedBackend(t)
			b.network = "isolated-net"
			removed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/_ping" {
					w.Header().Set("API-Version", "1.51")
					_, _ = w.Write([]byte("OK"))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/containers/json") {
					if !strings.Contains(r.URL.Query().Get("filters"), "isolated-net") {
						t.Error("missing network ownership")
					}
					_ = json.NewEncoder(w).Encode([]any{map[string]any{"Id": "relay", "Labels": map[string]string{"minikube-cloud-lab.service-proxy": "true", "minikube-cloud-lab.cluster": "demo", "minikube-cloud-lab.service-uid": "one", "minikube-cloud-lab.network": "isolated-net"}}})
					return
				}
				if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/relay") {
					if broken {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = w.Write([]byte(`{"message":"cleanup failed"}`))
						return
					}
					removed = true
					w.WriteHeader(http.StatusNoContent)
					return
				}
				t.Errorf("unexpected request %s %s", r.Method, r.URL)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			t.Setenv("DOCKER_HOST", strings.Replace(server.URL, "http://", "tcp://", 1))
			t.Setenv("DOCKER_API_VERSION", "1.51")
			err := b.DeleteCluster(t.Context(), "demo")
			if broken && err == nil || !broken && (err != nil || !removed) {
				t.Fatalf("cleanup removed=%v error=%v", removed, err)
			}
		})
	}
}
