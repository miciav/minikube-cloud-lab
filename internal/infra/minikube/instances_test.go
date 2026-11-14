package minikube

import (
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"

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
