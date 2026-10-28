package cloud

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
	"github.com/miciav/minikube-cloud-lab/internal/serviceexposure"
)

func TestDashboardFixedServiceAndLifecycle(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "not-ready", "invalid-json", "invalid-port", "wrong-port", "wrong-node-port", "exposure-error", "unknown-cluster", "creating", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			s, err := NewService(fake.New(), t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			s.state.Clusters = []Cluster{{Name: "demo", Status: "RUNNING", CreatedAt: time.Now()}}
			if mode == "creating" {
				s.state.Clusters[0].Status = "CREATING"
			}
			if mode == "unknown-cluster" {
				s.state.Clusters = nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/namespaces/yakd-dashboard/services/yakd-dashboard":
					if mode == "missing" {
						http.Error(w, "not found", http.StatusNotFound)
						return
					}
					annotation := `[{"servicePort":80,"nodePort":32011,"hostPort":32011}]`
					switch mode {
					case "invalid-json":
						annotation = "oops"
					case "invalid-port":
						annotation = `[{"servicePort":80,"nodePort":32011,"hostPort":70000}]`
					case "wrong-port":
						annotation = `[{"servicePort":81,"nodePort":32011,"hostPort":32011}]`
					case "wrong-node-port":
						annotation = `[{"servicePort":80,"nodePort":32012,"hostPort":32011}]`
					}
					svc := corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "yakd-dashboard", Namespace: "yakd-dashboard", Annotations: map[string]string{serviceexposure.HostEndpointsAnnotation: annotation}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, Ports: []corev1.ServicePort{{Port: 80, NodePort: 32011}}}}
					if mode == "exposure-error" {
						svc.Annotations[serviceexposure.ExposureErrorAnnotation] = "error"
					}
					_ = json.NewEncoder(w).Encode(svc)
				case "/apis/apps/v1/namespaces/yakd-dashboard/deployments/yakd-dashboard":
					ready := int32(1)
					if mode == "not-ready" {
						ready = 0
					}
					if mode == "deleted" {
						s.mu.Lock()
						s.state.Clusters = nil
						s.mu.Unlock()
					}
					_ = json.NewEncoder(w).Encode(appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, Status: appsv1.DeploymentStatus{AvailableReplicas: ready}})
				default:
					t.Errorf("arbitrary proxy target %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			cfg := clientapi.NewConfig()
			cfg.Clusters["demo"] = &clientapi.Cluster{Server: server.URL}
			cfg.Contexts["demo"] = &clientapi.Context{Cluster: "demo"}
			cfg.CurrentContext = "demo"
			dir := filepath.Join(s.dir, "minikube")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := clientcmd.WriteToFile(*cfg, filepath.Join(dir, "kubeconfig")); err != nil {
				t.Fatal(err)
			}
			got, err := s.Dashboard(t.Context(), "demo")
			switch mode {
			case "valid":
				if err != nil || got.Port != 32011 {
					t.Fatalf("%+v %v", got, err)
				}
			case "unknown-cluster", "deleted":
				if !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
			case "creating":
				if !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			default:
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("accepted invalid endpoint: %+v %v", got, err)
				}
			}
		})
	}
}
