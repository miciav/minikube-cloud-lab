package minikube

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"k8s.io/apimachinery/pkg/util/wait"
)

func rootlessLinkScript(mac string) (string, error) {
	address, err := net.ParseMAC(mac)
	if err != nil || len(address) != 6 {
		return "", fmt.Errorf("invalid runtime MAC address %q", mac)
	}
	mac = address.String()
	// Identical machine IDs in rootless nodes otherwise produce identical persistent MACs.
	return fmt.Sprintf("mkdir -p /etc/systemd/network && printf '%%s' '[Match]\nOriginalName=eth0\n[Link]\nMACAddressPolicy=none\nMACAddress=%s\n' > /etc/systemd/network/10-cloud-lab.link && ip link set dev eth0 address %s", mac, mac), nil
}

func restoreRootlessMAC(ctx context.Context, name, network string) error {
	client, err := dockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	observed, err := client.ContainerInspect(ctx, name)
	if err != nil {
		return err
	}
	if observed.NetworkSettings == nil || observed.NetworkSettings.Networks[network] == nil {
		return fmt.Errorf("node %s has no runtime network %s", name, network)
	}
	script, err := rootlessLinkScript(observed.NetworkSettings.Networks[network].MacAddress)
	if err != nil {
		return err
	}
	if err := execRoot(ctx, client, name, nil, script); err != nil {
		return fmt.Errorf("preserve rootless MAC: %w", err)
	}
	return nil
}

// execRoot runs script as root inside a container through the API and waits for success.
func execRoot(ctx context.Context, client *dockerclient.Client, name string, env []string, script string) error {
	task, err := client.ContainerExecCreate(ctx, name, container.ExecOptions{User: "0", Env: env, Cmd: []string{"sh", "-c", script}})
	if err != nil {
		return err
	}
	if err := client.ContainerExecStart(ctx, task.ID, container.ExecStartOptions{Detach: true}); err != nil {
		return err
	}
	return wait.PollUntilContextTimeout(ctx, 20*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		status, err := client.ContainerExecInspect(ctx, task.ID)
		if err != nil {
			return false, err
		}
		if status.Running {
			return false, nil
		}
		if status.ExitCode != 0 {
			return false, fmt.Errorf("command exited %d", status.ExitCode)
		}
		return true, nil
	})
}
