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
	for _, entry := range []struct{ name, ip string }{{"node-a", "172.30.0.2"}, {"node-b", "172.30.0.3"}, {"node-c", "fd00::4"}, {"starting", ""}} {
		addresses := []any{map[string]any{"type": "ExternalIP", "address": "203.0.113.1"}}
		if entry.ip != "" {
			addresses = append(addresses, map[string]any{"type": "InternalIP", "address": entry.ip})
		}
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": entry.name}, "status": map[string]any{"addresses": addresses}}})
	}
	for _, entry := range []struct{ name, kind, ports, problem string }{
		{"first", "LoadBalancer", `[{"servicePort":80,"nodePort":30080,"hostPort":40080}]`, ""},
		{"second", "LoadBalancer", `[{"servicePort":80,"nodePort":30081,"hostPort":40081}]`, ""},
		{"node", "NodePort", `[{"servicePort":80,"nodePort":30082,"hostPort":30082}]`, ""},
		{"failed", "NodePort", "", "port already allocated"},
		{"internal", "ClusterIP", "", ""},
		{"broken", "LoadBalancer", `invalid`, ""},
		{"invalid-port", "NodePort", `[{"hostPort":70000}]`, ""},
	} {
		ports := []any{map[string]any{"port": int64(80)}}
		switch entry.name {
		case "node":
			ports = []any{map[string]any{"nodePort": int64(31082)}, map[string]any{"nodePort": int64(31083)}}
		case "first":
			ports = []any{map[string]any{"nodePort": int64(31080)}}
		case "failed":
			ports = []any{map[string]any{"nodePort": int64(31084)}}
		case "invalid-port":
			ports = []any{map[string]any{"nodePort": int64(70000)}, map[string]any{"nodePort": int64(-1)}}
		}
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": entry.name, "namespace": "lesson", "annotations": map[string]any{endpoint: entry.ports, "minikube-cloud-lab.io/exposure-error": entry.problem}}, "spec": map[string]any{"type": entry.kind, "ports": ports}}})
	}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ServiceList", {Version: "v1", Resource: "nodes"}: "NodeList"}, objects...)
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
	if !strings.HasPrefix(out.String(), "NAMESPACE") || strings.Contains(out.String(), "equivalent:") {
		t.Fatalf("services must print the table directly: %s", out.String())
	}
	for _, want := range []string{"NAMESPACE", "LOCAL_ENDPOINT", "NODE_ENDPOINTS", "127.0.0.1:40080", "127.0.0.1:40081", "127.0.0.1:30082", "port already allocated", "invalid host-endpoints"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "127.0.0.1:70000") {
		t.Fatal("invalid endpoint advertised")
	}
	rows := map[string][]string{}
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "lesson" {
			rows[fields[1]] = fields
		}
	}
	for name, want := range map[string]string{
		"node":   "172.30.0.2:31082,172.30.0.3:31082,[fd00::4]:31082,172.30.0.2:31083,172.30.0.3:31083,[fd00::4]:31083",
		"first":  "172.30.0.2:31080,172.30.0.3:31080,[fd00::4]:31080",
		"failed": "172.30.0.2:31084,172.30.0.3:31084,[fd00::4]:31084",
		"second": "-", "internal": "-", "broken": "-", "invalid-port": "-",
	} {
		if row := rows[name]; len(row) < 5 || row[4] != want {
			t.Errorf("service %s: want node endpoints %q, got %v", name, want, row)
		}
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
	for _, resource := range []string{"services", "nodes"} {
		t.Run(resource, func(t *testing.T) {
			client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "services"}: "ServiceList", {Version: "v1", Resource: "nodes"}: "NodeList"})
			client.PrependReactor("list", resource, func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("API down") })
			root := newRoot(func(string, string) (dynamic.Interface, meta.RESTMapper, error) { return client, nil, nil })
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"services"})
			if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "API down") {
				t.Fatalf("hidden %s API error: %v", resource, err)
			}
		})
	}
}
