package labcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/miciav/minikube-cloud-lab/lab/manifests"
)

// The stock dynamic tracker cannot create objects through server-side apply.
func newFake() *dynamicfake.FakeDynamicClient {
	f := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	f.PrependReactor("patch", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		patch := action.(ktesting.PatchActionImpl)
		if patch.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		if patch.GetPatchOptions().FieldManager != "lab" {
			return true, nil, fmt.Errorf("missing lab field manager")
		}
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(patch.GetPatch()); err != nil {
			return true, nil, err
		}
		err := f.Tracker().Create(action.GetResource(), obj, action.GetNamespace())
		if apierrors.IsAlreadyExists(err) {
			err = f.Tracker().Update(action.GetResource(), obj, action.GetNamespace())
		}
		return true, obj, err
	})
	return f
}

func mapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "", Version: "v1"}, {Group: "apps", Version: "v1"}, {Group: "rbac.authorization.k8s.io", Version: "v1"}})
	for _, entry := range []struct {
		group, version, kind string
		scope                meta.RESTScope
	}{{"", "v1", "ServiceAccount", meta.RESTScopeNamespace}, {"", "v1", "ConfigMap", meta.RESTScopeNamespace}, {"apps", "v1", "Deployment", meta.RESTScopeNamespace}, {"rbac.authorization.k8s.io", "v1", "Role", meta.RESTScopeNamespace}, {"rbac.authorization.k8s.io", "v1", "RoleBinding", meta.RESTScopeNamespace}, {"rbac.authorization.k8s.io", "v1", "ClusterRole", meta.RESTScopeRoot}, {"rbac.authorization.k8s.io", "v1", "ClusterRoleBinding", meta.RESTScopeRoot}} {
		m.Add(schema.GroupVersionKind{Group: entry.group, Version: entry.version, Kind: entry.kind}, entry.scope)
	}
	return m
}

func run(t *testing.T, f *dynamicfake.FakeDynamicClient, args ...string) (string, error) {
	t.Helper()
	root := newRoot(func(string, string) (dynamic.Interface, meta.RESTMapper, error) { return f, mapper(), nil })
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func TestInstallAndUninstall(t *testing.T) {
	f := newFake()
	out, err := run(t, f, "install", "autoscaler")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "equivalent: kubectl apply") || !strings.Contains(out, "lab/manifests/autoscaler/") {
		t.Fatalf("equivalent: %s", out)
	}
	seen := map[string]bool{}
	for _, action := range f.Actions() {
		if action.GetVerb() != "patch" {
			continue
		}
		patch := action.(interface {
			GetName() string
			GetPatchType() types.PatchType
		})
		if patch.GetPatchType() != types.ApplyPatchType {
			t.Fatal("expected server-side apply")
		}
		seen[action.GetResource().Resource+"/"+patch.GetName()] = true
	}
	for _, object := range []string{"serviceaccounts/cluster-autoscaler", "clusterroles/cluster-autoscaler", "roles/cluster-autoscaler", "clusterrolebindings/cluster-autoscaler", "rolebindings/cluster-autoscaler", "configmaps/cluster-autoscaler-cloud-config", "deployments/cluster-autoscaler"} {
		if !seen[object] {
			t.Fatalf("object not applied: %s", object)
		}
	}
	if _, err := run(t, f, "uninstall", "autoscaler"); err != nil {
		t.Fatal(err)
	}
	deployment := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	if _, err := f.Resource(deployment).Namespace("kube-system").Get(context.Background(), "cluster-autoscaler", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deployment still present: %v", err)
	}
	if _, err := run(t, f, "uninstall", "autoscaler"); err != nil {
		t.Fatalf("uninstall is not idempotent: %v", err)
	}
}

func TestLoadReplicasAndStop(t *testing.T) {
	f := newFake()
	out, err := run(t, f, "load", "start", "--replicas", "6")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--replicas=6") {
		t.Fatalf("replica override missing: %s", out)
	}
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	object, err := f.Resource(gvr).Namespace("default").Get(context.Background(), "lab-load", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	replicas, _, err := unstructured.NestedInt64(object.Object, "spec", "replicas")
	if err != nil || replicas != 6 {
		t.Fatalf("replicas: %d %v", replicas, err)
	}
	if _, err := run(t, f, "load", "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Resource(gvr).Namespace("default").Get(context.Background(), "lab-load", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("load remains: %v", err)
	}
	if _, err := run(t, f, "load", "start", "--replicas", "0"); err == nil {
		t.Fatal("accepted nonpositive replicas")
	}
}

func TestEmbeddedFilesEqualDisk(t *testing.T) {
	err := fs.WalkDir(manifests.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		embedded, err := manifests.Files.ReadFile(path)
		if err != nil {
			return err
		}
		disk, err := os.ReadFile(filepath.Join("../../lab/manifests", path)) // #nosec G304 -- paths come from the embedded manifest filesystem.
		if err != nil {
			return err
		}
		if !bytes.Equal(embedded, disk) {
			t.Fatalf("manifest drift: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConnectionFailure(t *testing.T) {
	root := newRoot(func(string, string) (dynamic.Interface, meta.RESTMapper, error) {
		return nil, nil, errors.New("unavailable cluster")
	})
	root.SetErr(io.Discard)
	root.SetArgs([]string{"install", "autoscaler"})
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "unavailable cluster") {
		t.Fatalf("connection: %v", err)
	}
}

func TestRealKubeconfigContextOverride(t *testing.T) {
	wrongRequests := atomic.Int32{}
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		wrongRequests.Add(1)
		http.Error(w, "wrong context", http.StatusForbidden)
	}))
	defer wrong.Close()
	deleted := atomic.Bool{}
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api":
			_, _ = io.WriteString(w, `{"kind":"APIVersions","apiVersion":"v1","versions":["v1"]}`)
		case "/apis":
			_, _ = io.WriteString(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"apps","versions":[{"groupVersion":"apps/v1","version":"v1"}],"preferredVersion":{"groupVersion":"apps/v1","version":"v1"}}]}`)
		case "/api/v1":
			_, _ = io.WriteString(w, `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"v1","resources":[]}`)
		case "/apis/apps/v1":
			_, _ = io.WriteString(w, `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"apps/v1","resources":[{"name":"deployments","namespaced":true,"kind":"Deployment","verbs":["delete"]}]}`)
		case "/apis/apps/v1/namespaces/default/deployments/lab-load":
			if r.Method != http.MethodDelete {
				http.Error(w, "wrong method", http.StatusBadRequest)
				return
			}
			deleted.Store(true)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Success"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer selected.Close()
	config := clientapi.NewConfig()
	config.Clusters["other"] = &clientapi.Cluster{Server: wrong.URL}
	config.Clusters["target"] = &clientapi.Cluster{Server: selected.URL}
	config.Contexts["other"] = &clientapi.Context{Cluster: "other"}
	config.Contexts["target"] = &clientapi.Context{Cluster: "target"}
	config.CurrentContext = "other"
	path := filepath.Join(t.TempDir(), "config with 'quote'.yaml")
	if err := clientcmd.WriteToFile(*config, path); err != nil {
		t.Fatal(err)
	}
	root := NewRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--kubeconfig", path, "--context", "target", "load", "stop"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !deleted.Load() || wrongRequests.Load() != 0 {
		t.Fatal("context override sent the deletion to the wrong cluster")
	}
	if !strings.Contains(output.String(), "--context 'target'") || !strings.Contains(output.String(), `'"'"'`) {
		t.Fatalf("unsafe or incomplete equivalent: %s", output.String())
	}
}
