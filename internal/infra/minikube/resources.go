package minikube

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

func dockerClient() (*dockerclient.Client, error) {
	return dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
}

func forwardedAPIServer(ctx context.Context, name string) (string, error) {
	client, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	observed, err := client.ContainerInspect(ctx, name)
	if err != nil {
		return "", err
	}
	if observed.NetworkSettings != nil {
		for _, binding := range observed.NetworkSettings.Ports["8443/tcp"] {
			port, err := strconv.Atoi(binding.HostPort)
			if binding.HostIP == "127.0.0.1" && err == nil && port > 0 && port <= 65535 {
				return "https://" + net.JoinHostPort(binding.HostIP, binding.HostPort), nil
			}
		}
	}
	return "", fmt.Errorf("control plane %s has no valid localhost API port mapping", name)
}

// Upstream skips CPU quotas on Linux cgroup v2 because it checks cgroup v1 paths.
func setMachineResources(ctx context.Context, name string, shape infra.MachineShape) error {
	client, err := dockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	memory := int64(shape.MemoryMiB) * 1024 * 1024
	observed, err := client.ContainerInspect(ctx, name)
	if err != nil {
		return err
	}
	limits := container.Resources{CPUPeriod: 100000, CPUQuota: int64(shape.CPUs) * 100000, Memory: memory, MemorySwap: memory}
	if observed.HostConfig != nil {
		limits.PidsLimit = observed.HostConfig.PidsLimit
	}
	if observed.HostConfig != nil && observed.HostConfig.NanoCPUs > 0 {
		limits.CPUPeriod = 0
		limits.CPUQuota = 0
		limits.NanoCPUs = int64(shape.CPUs) * 1000000000
	}
	result, err := client.ContainerUpdate(ctx, name, container.UpdateConfig{Resources: limits})
	if err != nil {
		return err
	}
	if len(result.Warnings) > 0 {
		return fmt.Errorf("resource limits: %v", result.Warnings)
	}
	observed, err = client.ContainerInspect(ctx, name)
	if err != nil {
		return err
	}
	if observed.HostConfig == nil {
		return fmt.Errorf("container has no resource configuration")
	}
	got := observed.HostConfig
	if limits.PidsLimit != nil && (got.PidsLimit == nil || *got.PidsLimit != *limits.PidsLimit) {
		return fmt.Errorf("node PID limit was not preserved during resource update")
	}
	cpuOK := got.NanoCPUs == int64(shape.CPUs)*1000000000 || (got.CPUPeriod == 100000 && got.CPUQuota == int64(shape.CPUs)*100000)
	if !cpuOK || got.Memory != memory || got.MemorySwap != memory {
		return fmt.Errorf("requested CPU/memory limits were not applied; check cgroups delegation")
	}
	return nil
}
