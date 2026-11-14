package minikube

import (
	"fmt"
	"net/netip"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/serviceexposure"
)

const (
	instancePrefix   = "mcl-vm-"
	instanceLabel    = "minikube-cloud-lab.instance"
	instanceNetLabel = "minikube-cloud-lab.network"
	instanceIPLabel  = "minikube-cloud-lab.ip"
	instancePIDs     = 4096
)

// instanceAddress picks a host .200–.252 of the node half: below NFS (.253) and the cloud (.254),
// above the at most 32 nodes minikube numbers from .2.
func instanceAddress(n network.Inspect, reserved map[string]bool) (netip.Addr, error) {
	subnet, err := serviceexposure.ManagedSubnet(n)
	if err != nil {
		return netip.Addr{}, err
	}
	used := map[netip.Addr]bool{}
	for ip := range reserved {
		if addr, err := netip.ParseAddr(ip); err == nil {
			used[addr] = true
		}
	}
	for _, endpoint := range n.Containers {
		if member, err := netip.ParsePrefix(endpoint.IPv4Address); err == nil {
			used[member.Addr()] = true
		}
	}
	base := subnet.Addr().As4()
	for host := byte(200); host <= 252; host++ {
		base[3] = host
		if candidate := netip.AddrFrom4(base); !used[candidate] {
			return candidate, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no free instance address in .200-.252 of the lab network")
}

// instanceContainer mirrors the settings minikube uses for its node containers.
func instanceContainer(spec infra.InstanceSpec, image, networkName string, ip netip.Addr, podman bool) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	runtime, security, volume := "docker", []string{"seccomp=unconfined", "apparmor=unconfined"}, instancePrefix+spec.Name+":/var"
	if podman {
		// Podman mounts volumes noexec by default and has no AppArmor profile to lift.
		runtime, security, volume = "podman", []string{"seccomp=unconfined"}, volume+":exec"
	}
	memory := int64(spec.Shape.MemoryMiB) * 1024 * 1024
	pids := int64(instancePIDs)
	cfg := &container.Config{
		Image: image, Hostname: spec.Name, Tty: true, Env: []string{"container=" + runtime}, StopSignal: "SIGRTMIN+3",
		Labels: map[string]string{instanceLabel: spec.Name, instanceNetLabel: networkName, instanceIPLabel: ip.String()},
	}
	host := &container.HostConfig{
		Privileged: true, SecurityOpt: security, Tmpfs: map[string]string{"/tmp": "", "/run": ""},
		Binds:         []string{"/lib/modules:/lib/modules:ro", volume},
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources:     container.Resources{CPUPeriod: 100000, CPUQuota: int64(spec.Shape.CPUs) * 100000, Memory: memory, MemorySwap: memory, PidsLimit: &pids},
	}
	net := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{networkName: {IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: ip.String()}}}}
	return cfg, host, net
}
