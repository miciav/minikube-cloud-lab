package minikube

import (
	"context"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

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
