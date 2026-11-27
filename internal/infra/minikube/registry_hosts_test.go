package minikube

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"k8s.io/minikube/pkg/minikube/config"
)

func TestRegistryHostsTOML(t *testing.T) {
	want := `server = "http://corso.registry:5000"

[host."http://corso.registry:5000"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
`
	if got := string(registryHostsTOML("corso")); got != want {
		t.Fatalf("hosts.toml =\n%s\nwant\n%s", got, want)
	}
}

// nodeDocker records hosts files copied into and directories removed from node containers.
type nodeDocker struct {
	mu      sync.Mutex
	copied  map[string]map[string]string // container -> path -> content
	removed map[string][]string          // container -> argv
	fail    string                       // container whose calls fail
}

func (d *nodeDocker) serve(t *testing.T, registries string) {
	t.Helper()
	d.copied, d.removed = map[string]map[string]string{}, map[string][]string{}
	fakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		parts := strings.Split(r.URL.Path, "/")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = io.WriteString(w, registries)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			w.WriteHeader(http.StatusNoContent)
		case len(parts) > 2 && parts[len(parts)-2] == d.fail:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"boom"}`)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/archive"):
			node := parts[len(parts)-2]
			if d.copied[node] == nil {
				d.copied[node] = map[string]string{}
			}
			archive := tar.NewReader(r.Body)
			for {
				h, err := archive.Next()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(archive)
				d.copied[node]["/"+h.Name] = string(b)
			}
		case strings.HasSuffix(r.URL.Path, "/exec"):
			var body struct{ Cmd []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			d.removed[parts[len(parts)-2]] = body.Cmd
			_, _ = io.WriteString(w, `{"Id":"x"}`)
		case strings.HasSuffix(r.URL.Path, "/exec/x/start"):
		case strings.HasSuffix(r.URL.Path, "/exec/x/json"):
			_, _ = io.WriteString(w, `{"Running":false,"ExitCode":0}`)
		default:
			t.Errorf("unexpected Docker call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

// twoClusters saves a Docker-runtime and a containerd-runtime cluster.
func twoClusters(t *testing.T) *Infra {
	t.Helper()
	data := t.TempDir()
	i := &Infra{network: "lab", home: filepath.Join(data, "minikube")}
	for _, cc := range []*config.ClusterConfig{
		{Name: "dock", KubernetesConfig: config.KubernetesConfig{ContainerRuntime: "docker"}, Nodes: []config.Node{{ControlPlane: true}, {Name: "m02"}}},
		{Name: "ctr", KubernetesConfig: config.KubernetesConfig{ContainerRuntime: "containerd"}, Nodes: []config.Node{{ControlPlane: true}, {Name: "m02"}}},
	} {
		if err := config.SaveProfile(cc.Name, cc, i.home); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(i.home, "profiles", "stray"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return i
}

func addHosts(t *testing.T, i *Infra) {
	t.Helper()
	client, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	i.addRegistryHosts(context.Background(), client, "corso")
}

const hostsPath = "/etc/containerd/certs.d/corso.registry:5000/hosts.toml"

func TestRegistryHostsTargetsOnlyContainerdNodes(t *testing.T) {
	i := twoClusters(t)
	var d nodeDocker
	d.serve(t, `[]`)
	addHosts(t, i)
	if got := slices.Sorted(maps.Keys(d.copied)); !reflect.DeepEqual(got, []string{"ctr", "ctr-m02"}) {
		t.Fatalf("copied into %v", got)
	}
	for node, files := range d.copied {
		if files[hostsPath] != string(registryHostsTOML("corso")) {
			t.Fatalf("%s files: %v", node, files)
		}
	}
	d.serve(t, `[]`)
	d.fail = "ctr"
	addHosts(t, i) // one node failing is only logged
	if _, ok := d.copied["ctr-m02"][hostsPath]; !ok {
		t.Fatalf("a failing node stopped the others: %v", d.copied)
	}
}

func TestRegistryHostsRemovedOnDelete(t *testing.T) {
	i := twoClusters(t)
	var d nodeDocker
	d.serve(t, `[]`)
	if err := i.DeleteRegistry(context.Background(), "corso"); err != nil {
		t.Fatal(err)
	}
	argv := []string{"rm", "-rf", "--", "/etc/containerd/certs.d/corso.registry:5000"}
	want := map[string][]string{"ctr": argv, "ctr-m02": argv}
	if !reflect.DeepEqual(d.removed, want) {
		t.Fatalf("removed %v", d.removed)
	}
	d.serve(t, `[]`)
	d.fail = "ctr-m02"
	if err := i.DeleteRegistry(context.Background(), "corso"); err != nil {
		t.Fatalf("a failing node failed the delete: %v", err)
	}
}

func TestNewContainerdNodeTrustsRunningRegistries(t *testing.T) {
	var d nodeDocker
	d.serve(t, fmt.Sprintf(`[{"Id":"1","State":"running","Labels":{%q:"corso"}},{"Id":"2","State":"exited","Labels":{%q:"old"}}]`, registryLabel, registryLabel))
	i := &Infra{network: "lab"}
	ctr := &config.ClusterConfig{KubernetesConfig: config.KubernetesConfig{ContainerRuntime: "containerd"}}
	i.trustRegistries(context.Background(), ctr, "ctr-m03")
	if want := map[string]map[string]string{"ctr-m03": {hostsPath: string(registryHostsTOML("corso"))}}; !reflect.DeepEqual(d.copied, want) {
		t.Fatalf("copied %v", d.copied)
	}
	// Docker-runtime nodes trust the subnet; no lab network means no registries.
	fakeDockerAPI(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected Docker call %s %s", r.Method, r.URL.Path)
	})
	i.trustRegistries(context.Background(), &config.ClusterConfig{KubernetesConfig: config.KubernetesConfig{ContainerRuntime: "docker"}}, "dock")
	(&Infra{}).trustRegistries(context.Background(), ctr, "ctr")
	fakeDocker(t, nil)
	i.trustRegistries(context.Background(), ctr, "ctr") // a listing failure is only logged
}
