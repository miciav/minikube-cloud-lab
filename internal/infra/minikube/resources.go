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

func forwardedAPIServer(ctx context.Context, name string) (string, error) {
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
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
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	memory := int64(shape.MemoryMiB) * 1024 * 1024
	_, err = client.ContainerUpdate(ctx, name, container.UpdateConfig{Resources: container.Resources{NanoCPUs: int64(shape.CPUs) * 1000000000, Memory: memory, MemorySwap: memory}})
	return err
}
