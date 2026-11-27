package minikube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"golang.org/x/crypto/bcrypt"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

var registrySpec = infra.RegistrySpec{Name: "corso", PushUser: "push", PushPassword: "push-secret", PullUser: "pull", PullPassword: "pull-secret"}

func TestRegistryContainerConfig(t *testing.T) {
	ip := netip.MustParseAddr("172.30.0.205")
	cfg, host, net := registryContainer(registrySpec, "lab", "registries-volume", ip, false)
	wantLabels := map[string]string{registryLabel: "corso", instanceNetLabel: "lab", instanceIPLabel: "172.30.0.205"}
	if cfg.Image != "ghcr.io/project-zot/zot:v2.1.23@sha256:9d3de83ac2099e6f4843045b4c9b65b7e8baf40f92d28f54906dee9d4d390fd7" || !reflect.DeepEqual(cfg.Labels, wantLabels) ||
		!reflect.DeepEqual([]string(cfg.Cmd), []string{"serve", "/etc/zot/config.json"}) {
		t.Fatalf("config: %+v", cfg)
	}
	if host.Memory != 256<<20 || host.MemorySwap != 256<<20 || host.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatalf("host: %+v", host)
	}
	wantMount := mount.Mount{Type: mount.TypeVolume, Source: "registries-volume", Target: "/var/lib/registry", VolumeOptions: &mount.VolumeOptions{Subpath: "corso", NoCopy: true}}
	if !reflect.DeepEqual(host.Mounts, []mount.Mount{wantMount}) {
		t.Fatalf("mounts: %+v", host.Mounts)
	}
	endpoint := net.EndpointsConfig["lab"]
	if endpoint == nil || endpoint.IPAMConfig == nil || endpoint.IPAMConfig.IPv4Address != "172.30.0.205" || !reflect.DeepEqual(endpoint.Aliases, []string{"corso.registry"}) {
		t.Fatalf("network: %+v", net)
	}

	// Rootless Podman mounts the whole volume and keeps each registry in its own directory.
	_, host, _ = registryContainer(registrySpec, "lab", "registries-volume", ip, true)
	if len(host.Mounts) != 1 || host.Mounts[0].VolumeOptions == nil || host.Mounts[0].VolumeOptions.Subpath != "" || host.Mounts[0].Target != "/var/lib/registry" {
		t.Fatalf("rootless mounts: %+v", host.Mounts)
	}
	if registryRoot("corso", false) != "/var/lib/registry" || registryRoot("corso", true) != "/var/lib/registry/corso" {
		t.Fatalf("roots: %s %s", registryRoot("corso", false), registryRoot("corso", true))
	}
}

func TestRegistryConfigGrantsPushAndPull(t *testing.T) {
	config, htpasswd, err := registryConfig(registrySpec, "/var/lib/registry/corso")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Storage struct{ RootDirectory string }
		HTTP    struct {
			Port          string
			Compat        []string
			Auth          struct{ Htpasswd struct{ Path string } }
			AccessControl struct {
				Repositories map[string]struct {
					Policies []struct {
						Users, Actions []string
					}
					DefaultPolicy []string
				}
			}
		}
	}
	if err := json.Unmarshal(config, &parsed); err != nil {
		t.Fatalf("config JSON: %v", err)
	}
	if parsed.Storage.RootDirectory != "/var/lib/registry/corso" || parsed.HTTP.Port != "5000" || !reflect.DeepEqual(parsed.HTTP.Compat, []string{"docker2s2"}) || parsed.HTTP.Auth.Htpasswd.Path != "/etc/zot/htpasswd" {
		t.Fatalf("config: %s", config)
	}
	all := parsed.HTTP.AccessControl.Repositories["**"]
	type policy = struct{ Users, Actions []string }
	got := []policy{}
	for _, p := range all.Policies {
		got = append(got, policy{p.Users, p.Actions})
	}
	want := []policy{{[]string{"push"}, []string{"read", "create", "update"}}, {[]string{"pull"}, []string{"read"}}}
	if !reflect.DeepEqual(got, want) || len(all.DefaultPolicy) != 0 {
		t.Fatalf("policies: %s", config)
	}
	lines := strings.Split(strings.TrimSpace(string(htpasswd)), "\n")
	if len(lines) != 2 {
		t.Fatalf("htpasswd: %q", htpasswd)
	}
	for i, user := range [][2]string{{"push", "push-secret"}, {"pull", "pull-secret"}} {
		name, hash, _ := strings.Cut(lines[i], ":")
		if name != user[0] || bcrypt.CompareHashAndPassword([]byte(hash), []byte(user[1])) != nil {
			t.Fatalf("htpasswd line %d: %q", i, lines[i])
		}
	}
}

func TestRegistryAddressSkipsInstances(t *testing.T) {
	rows := []container.Summary{
		{State: "exited", Labels: map[string]string{instanceLabel: "old", instanceIPLabel: "172.30.0.200"}},
		{State: "running", Labels: map[string]string{instanceLabel: "web", instanceIPLabel: "172.30.0.201"}},
		{State: "running", Labels: map[string]string{registryLabel: "other", instanceIPLabel: "172.30.0.202"}},
	}
	// No name check: a registry replaces its own container.
	reserved, err := reservedAddresses(rows, "")
	if err != nil {
		t.Fatal(err)
	}
	lab := labNetwork("172.30.0.0/23", "172.30.0.2", "172.30.0.201", "172.30.0.202")
	seen := map[netip.Addr]bool{}
	for range 3 {
		got, err := instanceAddress(lab, reserved)
		if err != nil || seen[got] || got.As4()[3] < 203 || got.As4()[3] > 252 {
			t.Fatalf("address: %v %v (seen %v)", got, err, seen)
		}
		seen[got], reserved[got.String()] = true, true
	}
	// Instances see registry addresses too.
	reserved, err = reservedAddresses(rows, "new")
	if err != nil || !reserved["172.30.0.202"] {
		t.Fatalf("instance reservation: %v %v", reserved, err)
	}
	if _, err := reservedAddresses(rows, "old"); err == nil {
		t.Fatal("existing instance name accepted")
	}
}

// registryServer answers like zot for two repositories, one tagged through an image index.
func registryServer(t *testing.T) *httptest.Server {
	t.Helper()
	manifests := map[string]struct{ digest, body string }{
		"/v2/web/manifests/v1":          {"sha256:aaa", `{"config":{"size":100},"layers":[{"size":1000},{"size":2000}]}`},
		"/v2/web/manifests/v2":          {"sha256:bbb", `{"manifests":[{"digest":"sha256:ccc"}]}`},
		"/v2/web/manifests/sha256:ccc":  {"sha256:ccc", `{"config":{"size":10},"layers":[{"size":20}]}`},
		"/v2/team/api/manifests/latest": {"sha256:ddd", `{"config":{"size":1},"layers":[]}`},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "pull" || pass != "pull-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/_catalog":
			_, _ = io.WriteString(w, `{"repositories":["web","team/api","empty"]}`)
		case "/v2/web/tags/list":
			_, _ = io.WriteString(w, `{"name":"web","tags":["v2","v1"]}`)
		case "/v2/team/api/tags/list":
			_, _ = io.WriteString(w, `{"name":"team/api","tags":["latest"]}`)
		case "/v2/empty/tags/list":
			_, _ = io.WriteString(w, `{"name":"empty","tags":null}`)
		default:
			m, ok := manifests[r.URL.Path]
			if !ok || !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", m.digest)
			_, _ = io.WriteString(w, m.body)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRegistryImagesParsesCatalog(t *testing.T) {
	server := registryServer(t)
	data := t.TempDir()
	pushed := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	blob := filepath.Join(data, "web", "blobs", "sha256", "aaa")
	if err := os.MkdirAll(filepath.Dir(blob), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(blob, pushed, pushed); err != nil {
		t.Fatal(err)
	}
	got, err := registryImages(context.Background(), server.URL, registrySpec, data)
	if err != nil {
		t.Fatal(err)
	}
	want := []infra.RegistryImage{
		{Repository: "team/api", Tag: "latest", Bytes: 1},
		{Repository: "web", Tag: "v1", Bytes: 3100, Pushed: pushed},
		{Repository: "web", Tag: "v2", Bytes: 30},
	}
	if len(got) != len(want) {
		t.Fatalf("images: %+v", got)
	}
	for i := range want {
		if got[i].Repository != want[i].Repository || got[i].Tag != want[i].Tag || got[i].Bytes != want[i].Bytes || !got[i].Pushed.Equal(want[i].Pushed) {
			t.Fatalf("image %d: %+v, want %+v", i, got[i], want[i])
		}
	}

	wrong := registrySpec
	wrong.PullPassword = "nope"
	if _, err := registryImages(context.Background(), server.URL, wrong, data); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("bad credential: %v", err)
	}
}

func TestWaitRegistry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	if err := waitRegistry(context.Background(), server.URL, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("listening: %v", err)
	}
	err := waitRegistry(context.Background(), "http://127.0.0.1:1", func(context.Context) error { return errors.New("container exited with code 1") })
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("exited: %v", err)
	}
}

func TestRegistryBytesSumsDataDirectory(t *testing.T) {
	data := t.TempDir()
	i := &Infra{home: filepath.Join(data, "minikube")}
	if n, err := i.RegistryBytes(context.Background(), "corso"); err != nil || n != 0 {
		t.Fatalf("missing: %d %v", n, err)
	}
	dir := filepath.Join(data, "registries", "corso", "web", "blobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a": 10, "b": 32} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := i.RegistryBytes(context.Background(), "corso"); err != nil || n != 42 {
		t.Fatalf("bytes: %d %v", n, err)
	}
}

func TestRegistriesListAndRemove(t *testing.T) {
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "registries", "corso", "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	var removed []string
	fakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			if !strings.Contains(r.URL.Query().Get("filters"), registryLabel) {
				t.Errorf("filters: %s", r.URL.Query().Get("filters"))
			}
			_, _ = fmt.Fprintf(w, `[{"Id":"1","Names":["/%scorso"],"State":"running","Labels":{%q:"corso",%q:"172.30.0.203"}},{"Id":"2","Names":["/%sold"],"State":"exited","Labels":{%q:"old",%q:"172.30.0.204"}}]`,
				registryPrefix, registryLabel, instanceIPLabel, registryPrefix, registryLabel, instanceIPLabel)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			removed = append(removed, name)
			if name == registryPrefix+"gone" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"message":"no such container"}`)
			}
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	i := &Infra{network: "lab", home: filepath.Join(data, "minikube")}
	got, err := i.Registries(context.Background())
	want := []infra.RegistryInfo{{Name: "corso", IP: "172.30.0.203", Running: true}, {Name: "old", IP: "172.30.0.204"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("registries: %+v %v", got, err)
	}
	if err := i.StopRegistries(context.Background()); err != nil || !reflect.DeepEqual(removed, []string{"1", "2"}) {
		t.Fatalf("stop: %v %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(data, "registries", "corso")); err != nil {
		t.Fatalf("stop removed data: %v", err)
	}
	if err := i.DeleteRegistry(context.Background(), "corso"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "registries", "corso")); !os.IsNotExist(err) {
		t.Fatalf("data kept: %v", err)
	}
	if err := i.DeleteRegistry(context.Background(), "gone"); err != nil {
		t.Fatalf("absent registry: %v", err)
	}
}

func TestCreateRegistryNeedsVolumeAndNetwork(t *testing.T) {
	t.Setenv("CLOUD_REGISTRY_VOLUME", "")
	if _, err := (&Infra{network: "lab"}).CreateRegistry(context.Background(), registrySpec); err == nil || !strings.Contains(err.Error(), "CLOUD_REGISTRY_VOLUME") {
		t.Fatalf("no volume: %v", err)
	}
}
