package minikube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

func labNetwork(subnet string, members ...string) network.Inspect {
	n := network.Inspect{Driver: "bridge", IPAM: network.IPAM{Config: []network.IPAMConfig{{Subnet: subnet, Gateway: "172.30.0.1"}}}, Containers: map[string]network.EndpointResource{}}
	for i, ip := range members {
		n.Containers[fmt.Sprint(i)] = network.EndpointResource{IPv4Address: ip + "/23"}
	}
	return n
}

func TestInstanceAddress(t *testing.T) {
	got, err := instanceAddress(labNetwork("172.30.0.0/23", "172.30.0.2", "172.30.0.200"), map[string]bool{"172.30.0.201": true})
	if err != nil || got != netip.MustParseAddr("172.30.0.202") {
		t.Fatalf("address: %v %v", got, err)
	}
	var full []string
	for host := 200; host <= 252; host++ {
		full = append(full, fmt.Sprintf("172.30.0.%d", host))
	}
	if _, err := instanceAddress(labNetwork("172.30.0.0/23", full...), nil); err == nil || !strings.Contains(err.Error(), "no free instance address") {
		t.Fatalf("exhausted: %v", err)
	}
	if _, err := instanceAddress(labNetwork("172.30.0.0/24"), nil); err == nil || !strings.Contains(err.Error(), "/23") {
		t.Fatalf("subnet: %v", err)
	}
}

func TestInstanceContainerDocker(t *testing.T) {
	spec := infra.InstanceSpec{Name: "vm1", Shape: infra.MachineShape{CPUs: 2, MemoryMiB: 2048}}
	cfg, host, net := instanceContainer(spec, "kicbase@sha", "lab", netip.MustParseAddr("172.30.0.200"), false)
	wantLabels := map[string]string{instanceLabel: "vm1", instanceNetLabel: "lab", instanceIPLabel: "172.30.0.200"}
	if cfg.Image != "kicbase@sha" || cfg.Hostname != "vm1" || !cfg.Tty || !reflect.DeepEqual(cfg.Env, []string{"container=docker"}) || cfg.StopSignal != "SIGRTMIN+3" || !reflect.DeepEqual(cfg.Labels, wantLabels) {
		t.Fatalf("config: %+v", cfg)
	}
	if !host.Privileged || !reflect.DeepEqual(host.SecurityOpt, []string{"seccomp=unconfined", "apparmor=unconfined"}) || !reflect.DeepEqual(host.Tmpfs, map[string]string{"/tmp": "", "/run": ""}) ||
		!reflect.DeepEqual(host.Binds, []string{"/lib/modules:/lib/modules:ro", "mcl-vm-vm1:/var"}) || host.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatalf("host: %+v", host)
	}
	r := host.Resources
	if r.CPUPeriod != 100000 || r.CPUQuota != 200000 || r.Memory != 2147483648 || r.MemorySwap != 2147483648 || r.PidsLimit == nil || *r.PidsLimit != 4096 {
		t.Fatalf("resources: %+v", r)
	}
	if endpoint := net.EndpointsConfig["lab"]; endpoint == nil || endpoint.IPAMConfig == nil || endpoint.IPAMConfig.IPv4Address != "172.30.0.200" {
		t.Fatalf("network: %+v", net)
	}
}

func TestInstanceContainerPodman(t *testing.T) {
	spec := infra.InstanceSpec{Name: "vm1", Shape: infra.MachineShape{CPUs: 2, MemoryMiB: 2048}}
	cfg, host, _ := instanceContainer(spec, "kicbase@sha", "lab", netip.MustParseAddr("172.30.0.200"), true)
	if !reflect.DeepEqual(cfg.Env, []string{"container=podman"}) || !reflect.DeepEqual(host.SecurityOpt, []string{"seccomp=unconfined"}) || host.Binds[1] != "mcl-vm-vm1:/var:exec" {
		t.Fatalf("podman: %+v %+v", cfg.Env, host)
	}
}

func TestReservedAddressesKeepStoppedInstanceIPs(t *testing.T) {
	rows := []container.Summary{
		{State: "exited", Labels: map[string]string{instanceLabel: "old", instanceIPLabel: "172.30.0.200"}},
		{State: "running", Labels: map[string]string{instanceLabel: "web", instanceIPLabel: "172.30.0.201"}},
	}
	reserved, err := reservedAddresses(rows, "new")
	if err != nil || !reserved["172.30.0.200"] || !reserved["172.30.0.201"] {
		t.Fatalf("reserved: %v %v", reserved, err)
	}
	// The stopped instance is absent from the network view, so only the reservation protects its IP.
	got, err := instanceAddress(labNetwork("172.30.0.0/23", "172.30.0.201"), reserved)
	if err != nil || got != netip.MustParseAddr("172.30.0.202") {
		t.Fatalf("address: %v %v", got, err)
	}
	if _, err := reservedAddresses(rows, "old"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing name: %v", err)
	}
}

func TestWaitSSHStopsWhenContainerExits(t *testing.T) {
	started := time.Now()
	err := waitSSH(context.Background(), "127.0.0.1:1", func(context.Context) error { return errors.New("container exited with code 1") })
	if err == nil || !strings.Contains(err.Error(), "container exited with code 1") || time.Since(started) > 5*time.Second {
		t.Fatalf("exited container: %v after %s", err, time.Since(started))
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err := waitSSH(context.Background(), listener.Addr().String(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("listening: %v", err)
	}
}

// fakeDocker serves the Docker API paths in routes (matched by suffix) and points the SDK at it.
func fakeDocker(t *testing.T, routes map[string]string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			return
		}
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				_, _ = io.WriteString(w, body)
				return
			}
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"boom"}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+server.Listener.Addr().String())
}

var instanceSpec = infra.InstanceSpec{Name: "vm1", Image: "debian-12", Shape: infra.MachineShape{CPUs: 2, MemoryMiB: 2048}}

func TestCreateInstanceNamesTheFailedStep(t *testing.T) {
	fakeDocker(t, nil)
	if _, err := (&Infra{network: "lab"}).CreateInstance(context.Background(), instanceSpec); err == nil || !strings.Contains(err.Error(), "inspect Docker host") {
		t.Fatalf("info failure: %v", err)
	}
}

func TestCreateInstanceRefusesStaleVolume(t *testing.T) {
	fakeDocker(t, map[string]string{
		"/info":               `{"NCPU": 8, "MemTotal": 17179869184}`,
		"/containers/json":    `[]`,
		"/volumes/mcl-vm-vm1": `{"Name": "mcl-vm-vm1"}`,
	})
	if _, err := (&Infra{network: "lab"}).CreateInstance(context.Background(), instanceSpec); err == nil || !strings.Contains(err.Error(), "volume mcl-vm-vm1 already exists") {
		t.Fatalf("stale volume: %v", err)
	}
}
