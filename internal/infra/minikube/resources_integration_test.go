//go:build integration

package minikube

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

func TestMachineResourceCalibration(t *testing.T) {
	ctx := context.Background()
	host, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", `{{(index .Endpoints "docker").Host}}`).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", strings.TrimSpace(string(host)))
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	created, err := client.ContainerCreate(ctx, &container.Config{Image: "minikube-cloud-lab/cloud:dev", Entrypoint: []string{"sleep"}, Cmd: []string{"60"}}, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Error(err)
		}
	})
	if err := setMachineResources(ctx, created.ID, infra.MachineShape{CPUs: 2, MemoryMiB: 4096}); err != nil {
		t.Fatal(err)
	}
	if err := client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	observed, err := client.ContainerInspect(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.HostConfig.NanoCPUs != 2000000000 || observed.HostConfig.Memory != 4294967296 {
		t.Fatalf("runtime limits not applied: %+v", observed.HostConfig.Resources)
	}
}
