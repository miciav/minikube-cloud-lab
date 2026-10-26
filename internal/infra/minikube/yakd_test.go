package minikube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/minikube/pkg/minikube/config"
)

func yakdConfig(t *testing.T, server string) *config.ClusterConfig {
	t.Helper()
	_, _ = isolatedBackend(t)
	cc := &config.ClusterConfig{Name: "demo", KubernetesConfig: config.KubernetesConfig{KubernetesVersion: "v1.35.6", ImageRepository: "registry.k8s.io"}}
	if err := config.SaveProfile(cc.Name, cc); err != nil {
		t.Fatal(err)
	}
	cfg := clientapi.NewConfig()
	cfg.Clusters["demo"] = &clientapi.Cluster{Server: server}
	cfg.Contexts["demo"] = &clientapi.Context{Cluster: "demo"}
	cfg.Clusters["unrelated"] = &clientapi.Cluster{Server: "http://127.0.0.1:1"}
	cfg.Contexts["unrelated"] = &clientapi.Context{Cluster: "unrelated"}
	cfg.CurrentContext = "unrelated"
	if err := clientcmd.WriteToFile(*cfg, os.Getenv("KUBECONFIG")); err != nil {
		t.Fatal(err)
	}
	return cc
}

func TestYAKDAppliesBundledAddonInManagedContext(t *testing.T) {
	paths := []string{
		"/api/v1/namespaces/yakd-dashboard",
		"/api/v1/namespaces/yakd-dashboard/serviceaccounts/yakd-dashboard",
		"/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/yakd-dashboard",
		"/api/v1/namespaces/yakd-dashboard/services/yakd-dashboard",
		"/apis/apps/v1/namespaces/yakd-dashboard/deployments/yakd-dashboard",
	}
	requests := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		if r.Method != http.MethodPatch || r.Header.Get("Content-Type") != "application/apply-patch+yaml" || r.URL.Query().Get("fieldManager") != "cloud-yakd" {
			t.Errorf("not server-side apply: %s %s", r.Method, r.URL)
		}
		var obj json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case paths[2]:
			var binding rbacv1.ClusterRoleBinding
			if err := json.Unmarshal(obj, &binding); err != nil {
				t.Error(err)
			}
			if binding.RoleRef.Name != "cluster-admin" || len(binding.Subjects) != 1 || binding.Subjects[0].Name != "yakd-dashboard" || binding.Subjects[0].Namespace != "yakd-dashboard" {
				t.Errorf("invalid addon RBAC: %+v", binding)
			}
		case paths[3]:
			var svc corev1.Service
			if err := json.Unmarshal(obj, &svc); err != nil {
				t.Error(err)
			}
			if svc.Spec.Type != corev1.ServiceTypeNodePort || len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 80 || svc.Spec.Ports[0].TargetPort.IntVal != 8080 {
				t.Errorf("invalid addon service: %+v", svc.Spec)
			}
		case paths[4]:
			var deploy appsv1.Deployment
			if err := json.Unmarshal(obj, &deploy); err != nil {
				t.Error(err)
			}
			if len(deploy.Spec.Template.Spec.Containers) != 1 {
				t.Error("missing YAKD container")
				break
			}
			c := deploy.Spec.Template.Spec.Containers[0]
			if c.Image != "ghcr.io/manusa/yakd:0.0.9@sha256:8de49dcce135409b0b9ffea73e0822420eef5bfba5bc14128396647db8c8ad93" || c.Resources.Requests.Memory().Value() != 134217728 || c.Resources.Limits.Memory().Value() != 268435456 || deploy.Spec.Template.Spec.ServiceAccountName != "yakd-dashboard" {
				t.Errorf("invalid bundled deployment: %+v", deploy.Spec.Template.Spec)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(obj)
	}))
	defer server.Close()
	cc := yakdConfig(t, server.URL)
	for range 2 {
		if err := enableYAKD(t.Context(), cc); err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			select {
			case got := <-requests:
				if got != path {
					t.Fatalf("addon order/path: %s want %s", got, path)
				}
			default:
				t.Fatalf("missing addon request %s", path)
			}
		}
	}
	saved, err := config.Load("demo")
	if err != nil || !saved.Addons["yakd"] {
		t.Fatalf("addon not persisted: %+v, %v", saved, err)
	}
}

func TestYAKDFailuresReturnWithoutEnablingAddon(t *testing.T) {
	for _, mode := range []string{"namespace-denied", "deployment-denied", "canceled", "invalid-kubeconfig"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "namespace-denied" || strings.Contains(r.URL.Path, "/deployments/") {
					http.Error(w, "addon denied", http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Success"}`))
			}))
			defer server.Close()
			cc := yakdConfig(t, server.URL)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if mode == "invalid-kubeconfig" {
				t.Setenv("KUBECONFIG", t.TempDir()+"/missing")
			}
			if err := enableYAKD(ctx, cc); err == nil {
				t.Fatal("addon failure reported success")
			}
			saved, err := config.Load("demo")
			if err != nil || saved.Addons["yakd"] {
				t.Fatalf("failed addon marked enabled: %+v, %v", saved, err)
			}
		})
	}
}
