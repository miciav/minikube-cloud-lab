package minikube

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestApplyManifestAppliesEveryDocument(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"x"}}`))
	}))
	defer server.Close()
	client, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n---\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n  namespace: a\n")
	if err := applyManifest(t.Context(), client, manifest, "test", "test"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "PATCH /api/v1/namespaces/a" || paths[1] != "PATCH /api/v1/namespaces/a/configmaps/b" {
		t.Fatalf("applied %v", paths)
	}
}
