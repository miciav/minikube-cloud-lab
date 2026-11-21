package minikube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os/exec"
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

// fakeDockerAPI points the SDK at handler, answering version negotiation itself.
func fakeDockerAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+server.Listener.Addr().String())
}

// fakeDocker serves the Docker API paths in routes (matched by suffix); others fail with 500.
func fakeDocker(t *testing.T, routes map[string]string) {
	t.Helper()
	fakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				_, _ = io.WriteString(w, body)
				return
			}
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"boom"}`)
	})
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

func TestInstanceImageRef(t *testing.T) {
	if ref, kicbase, err := instanceImageRef("ubuntu-26.04"); err != nil || kicbase || ref != "ghcr.io/miciav/minikube-cloud-lab/instance-ubuntu:26.04" {
		t.Fatalf("default: %q %v %v", ref, kicbase, err)
	}
	t.Setenv("CLOUD_UBUNTU_IMAGE", "example/ubuntu:test")
	if ref, _, err := instanceImageRef("ubuntu-26.04"); err != nil || ref != "example/ubuntu:test" {
		t.Fatalf("override: %q %v", ref, err)
	}
	if _, kicbase, err := instanceImageRef("debian-12"); err != nil || !kicbase {
		t.Fatalf("kicbase: %v %v", kicbase, err)
	}
	if _, _, err := instanceImageRef("fedora"); err == nil {
		t.Fatal("accepted an unknown image")
	}
}

// pullingDocker reports ref missing until a pull answers with stream.
func pullingDocker(t *testing.T, stream string) *int {
	t.Helper()
	return pullingDockerThat(t, stream, true)
}

// pullingDockerThat lets a test keep the image missing even after a pull.
func pullingDockerThat(t *testing.T, stream string, appears bool) *int {
	t.Helper()
	pulls := new(int)
	fakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			*pulls++
			_, _ = io.WriteString(w, stream)
		case strings.HasSuffix(r.URL.Path, "/images/example/ubuntu:test/json") && *pulls > 0 && appears:
			_, _ = io.WriteString(w, `{"Id": "sha256:abc"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such image"}`)
		}
	})
	return pulls
}

func TestEnsureImagePullsMissingImage(t *testing.T) {
	pulls := pullingDocker(t, `{"status":"Pulling from example/ubuntu"}`+"\n"+`{"status":"Downloaded newer image"}`+"\n")
	client, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := ensureImage(context.Background(), client, "example/ubuntu:test"); err != nil || *pulls != 1 {
		t.Fatalf("pull: %v after %d pulls", err, *pulls)
	}
	if err := ensureImage(context.Background(), client, "example/ubuntu:test"); err != nil || *pulls != 1 {
		t.Fatalf("present image pulled again: %v after %d pulls", err, *pulls)
	}
}

func TestEnsureImageReportsPullErrors(t *testing.T) {
	pullingDocker(t, `{"error":"denied: requested access to the resource is denied","errorDetail":{"message":"denied: requested access to the resource is denied"}}`+"\n")
	client, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := ensureImage(context.Background(), client, "example/ubuntu:test"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("pull error: %v", err)
	}
}

func TestEnsureImageReportsTheLegacyErrorField(t *testing.T) {
	pullingDockerThat(t, `{"error":"denied: requested access to the resource is denied"}`+"\n", false)
	client, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := ensureImage(context.Background(), client, "example/ubuntu:test"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("legacy error field: %v", err)
	}
}

func TestEnsureImageFailsWhenPullLeavesNoImage(t *testing.T) {
	pullingDockerThat(t, `{"status":"Pulling from example/ubuntu"}`+"\n", false)
	client, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := ensureImage(context.Background(), client, "example/ubuntu:test"); err == nil || !strings.Contains(err.Error(), "missing after pull") {
		t.Fatalf("truncated pull: %v", err)
	}
}

func TestEnsureImageLogsThePull(t *testing.T) {
	pullingDocker(t, `{"status":"Downloaded newer image"}`+"\n")
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	client, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := ensureImage(context.Background(), client, "example/ubuntu:test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "pulling instance image") || !strings.Contains(logs.String(), "example/ubuntu:test") {
		t.Fatalf("logs: %s", logs.String())
	}
}

func TestBootstrapUsesItsInterpreter(t *testing.T) {
	for _, tc := range []struct{ script, want string }{
		{"#!/usr/bin/env bash\nitems=('two words' three)\nprintf '%s' \"${items[0]}\"", "two words"},
		{"printf '%s' plain", "plain"},
	} {
		argv := bootstrapCommand(tc.script)
		cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...) // #nosec G204 -- test scripts are literals.
		cmd.Dir = t.TempDir()
		if out, err := cmd.CombinedOutput(); err != nil || string(out) != tc.want {
			t.Fatalf("bootstrap output %q, error %v", out, err)
		}
	}
}
