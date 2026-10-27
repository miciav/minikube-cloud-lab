package serviceexposure

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type dockerRecord struct {
	container.Config
	HostConfig       container.HostConfig
	NetworkingConfig network.NetworkingConfig
	id, ip           string
	running          bool
	observed         nat.PortMap
}
type dockerFixture struct {
	mu                                sync.Mutex
	objects                           map[string]*dockerRecord
	network                           network.Inspect
	nextPort                          int
	client                            *dockerclient.Client
	failPath                          string
	created, deleted                  int
	conflict, wildcard, removeFailure bool
}

func newDockerFixture(t *testing.T) (*dockerFixture, *Manager) {
	t.Helper()
	f := &dockerFixture{objects: map[string]*dockerRecord{}, nextPort: 40080, network: network.Inspect{Name: "lab-net", Driver: "bridge", EnableIPv4: true, IPAM: network.IPAM{Driver: "default", Config: []network.IPAMConfig{{Subnet: "172.30.0.0/23", Gateway: "172.30.0.1"}}}, Containers: map[string]network.EndpointResource{"node": {IPv4Address: "172.30.0.2/23"}, "cloud": {IPv4Address: "172.30.0.254/23"}}}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	client, err := dockerclient.NewClientWithOpts(dockerclient.WithHost(strings.Replace(server.URL, "http://", "tcp://", 1)), dockerclient.WithVersion("1.51"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	f.client = client
	return f, NewManager(client, "lab-net", "sha256:relay-image")
}

func (f *dockerFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1.51")
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
	}
	if path == f.failPath {
		fail(503, "Docker unavailable")
		return
	}
	switch {
	case path == "/networks/lab-net":
		n := f.network
		n.Containers = map[string]network.EndpointResource{}
		for id, v := range f.network.Containers {
			n.Containers[id] = v
		}
		for id, v := range f.objects {
			if v.ip != "" {
				n.Containers[id] = network.EndpointResource{IPv4Address: v.ip + "/23"}
			}
		}
		_ = json.NewEncoder(w).Encode(n)
	case path == "/containers/json":
		args, err := filters.FromJSON(r.URL.Query().Get("filters"))
		if err != nil {
			fail(400, "invalid filters")
			return
		}
		rows := []container.Summary{}
		for id, v := range f.objects {
			if args.MatchKVList("label", v.Labels) {
				rows = append(rows, container.Summary{ID: id, Labels: v.Labels, Image: v.Image})
			}
		}
		_ = json.NewEncoder(w).Encode(rows)
	case path == "/containers/create":
		var record dockerRecord
		if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
			fail(400, err.Error())
			return
		}
		endpoint := record.NetworkingConfig.EndpointsConfig["lab-net"]
		if endpoint == nil || endpoint.IPAMConfig == nil {
			fail(400, "missing explicit relay network IP")
			return
		}
		f.created++
		record.id = fmt.Sprintf("proxy-%d", f.created)
		record.ip = endpoint.IPAMConfig.IPv4Address
		record.observed = nat.PortMap{}
		f.objects[record.id] = &record
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(container.CreateResponse{ID: record.id})
	case strings.HasPrefix(path, "/containers/"):
		tail := strings.TrimPrefix(path, "/containers/")
		id, action, _ := strings.Cut(tail, "/")
		v, ok := f.objects[id]
		if !ok {
			fail(404, "no such container")
			return
		}
		switch {
		case r.Method == http.MethodDelete:
			if f.removeFailure {
				fail(500, "removal failed")
				return
			}
			delete(f.objects, id)
			f.deleted++
			w.WriteHeader(http.StatusNoContent)
		case action == "start":
			if f.conflict {
				fail(409, "port is already allocated")
				return
			}
			v.running = true
			previous := v.observed
			v.observed = nat.PortMap{}
			for port, bindings := range v.HostConfig.PortBindings {
				for _, b := range bindings {
					if b.HostPort == "" {
						if len(previous[port]) == 1 {
							b.HostPort = previous[port][0].HostPort
						} else {
							b.HostPort = strconv.Itoa(f.nextPort)
							f.nextPort++
						}
					}
					if f.wildcard {
						b.HostIP = "0.0.0.0"
					}
					v.observed[port] = append(v.observed[port], b)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case action == "json":
			settings := &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{"lab-net": {IPAddress: v.ip}}}
			settings.Ports = v.observed
			_ = json.NewEncoder(w).Encode(container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id, State: &container.State{Running: v.running}, HostConfig: &v.HostConfig}, Config: &v.Config, NetworkSettings: settings})
		default:
			fail(500, "unexpected container request: "+r.Method+" "+path)
		}
	default:
		fail(500, "unexpected Docker request: "+r.Method+" "+path)
	}
}
