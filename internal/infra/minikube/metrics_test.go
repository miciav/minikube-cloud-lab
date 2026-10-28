package minikube

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

func TestMetricsManifestInstallFailure(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "denied"}[denied], func(t *testing.T) {
			count := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				if denied {
					http.Error(w, "denied", http.StatusForbidden)
					return
				}
				var obj json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&obj)
				if strings.Contains(r.URL.Path, "/deployments/") {
					var d appsv1.Deployment
					_ = json.Unmarshal(obj, &d)
					c := d.Spec.Template.Spec.Containers[0]
					if !strings.Contains(c.Image, "v0.9.0@sha256:d9862115") || !strings.Contains(strings.Join(c.Args, " "), "--kubelet-insecure-tls") || d.Namespace != "kube-system" {
						t.Errorf("bad metrics deployment %+v", d)
					}
				}
				if r.Method != http.MethodPatch || r.URL.Query().Get("fieldManager") != "cloud-metrics" {
					t.Errorf("not server apply: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(obj)
			}))
			defer server.Close()
			cc := yakdConfig(t, server.URL)
			err := enableMetrics(t.Context(), cc)
			if denied && err == nil {
				t.Fatal("install failure swallowed")
			}
			if !denied && (err != nil || count != 9) {
				t.Fatalf("requests %d err %v", count, err)
			}
		})
	}
}
