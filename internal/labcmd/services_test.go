package labcmd

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestServicesReadsEndpointsAndRespectsConnection(t *testing.T) {
	const endpoint = "minikube-cloud-lab.io/host-endpoints"
	objects := []runtime.Object{}
	for _, entry := range []struct{ name, kind, ports, problem string }{
		{"first", "LoadBalancer", `[{"servicePort":80,"nodePort":30080,"hostPort":40080}]`, ""},
		{"second", "LoadBalancer", `[{"servicePort":80,"nodePort":30081,"hostPort":40081}]`, ""},
		{"node", "NodePort", `[{"servicePort":80,"nodePort":30082,"hostPort":30082}]`, ""},
		{"failed", "NodePort", "", "port already allocated"},
		{"internal", "ClusterIP", "", ""},
		{"broken", "LoadBalancer", `invalid`, ""},
		{"invalid-port", "NodePort", `[{"hostPort":70000}]`, ""},
	} {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": entry.name, "namespace": "lesson", "annotations": map[string]any{endpoint: entry.ports, "minikube-cloud-lab.io/exposure-error": entry.problem}}, "spec": map[string]any{"type": entry.kind}}})
	}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ServiceList"}, objects...)
	root := newRoot(func(path, context string) (dynamic.Interface, meta.RESTMapper, error) {
		if path != "private-config" || context != "lesson" {
			t.Fatalf("connection flags lost: %q %q", path, context)
		}
		return client, mapper(), nil
	})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--kubeconfig", "private-config", "--context", "lesson", "services"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kubectl --kubeconfig 'private-config' --context 'lesson' get services -A -o json", "NAMESPACE", "LOCAL_ENDPOINT", "127.0.0.1:40080", "127.0.0.1:40081", "127.0.0.1:30082", "port already allocated", "invalid host-endpoints"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "127.0.0.1:70000") {
		t.Fatal("invalid endpoint advertised")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "list" || action.GetNamespace() != "" {
			t.Fatalf("view was not read-only/all namespaces: %+v", action)
		}
	}
}

func TestServicesReportsConnectionAndAPIErrors(t *testing.T) {
	root := newRoot(func(string, string) (dynamic.Interface, meta.RESTMapper, error) {
		return nil, nil, errors.New("bad credentials")
	})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"services"})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "bad credentials") {
		t.Fatalf("hidden connection error: %v", err)
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "services"}: "ServiceList"})
	client.PrependReactor("list", "services", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("API down") })
	root = newRoot(func(string, string) (dynamic.Interface, meta.RESTMapper, error) { return client, nil, nil })
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"services"})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "API down") {
		t.Fatalf("hidden API error: %v", err)
	}
}
