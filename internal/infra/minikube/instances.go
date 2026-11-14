package minikube

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	dockerclient "github.com/docker/docker/client"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/minikube/pkg/drivers/kic"

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

// reservedAddresses keeps every instance's IP, including stopped ones that left the
// network view but take their address back on start.
func reservedAddresses(rows []container.Summary, name string) (map[string]bool, error) {
	reserved := map[string]bool{}
	for _, c := range rows {
		if c.Labels[instanceLabel] == name {
			return nil, fmt.Errorf("container %s already exists", instancePrefix+name)
		}
		reserved[c.Labels[instanceIPLabel]] = true
	}
	return reserved, nil
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

var _ infra.Instances = (*Infra)(nil)

// prepareInstance gives each instance its own machine-id (kicbase ships one), restarts journald so
// logs follow the new ID, and installs the student key.
const prepareInstance = `rm -f /etc/machine-id /var/lib/dbus/machine-id && systemd-machine-id-setup >/dev/null &&
systemctl restart systemd-journald &&
install -d -m 700 -o "$SSH_USER" -g "$(id -gn "$SSH_USER")" "/home/$SSH_USER/.ssh" &&
printf '%s\n' "$AUTHORIZED_KEY" > "/home/$SSH_USER/.ssh/authorized_keys" &&
chown "$SSH_USER:" "/home/$SSH_USER/.ssh/authorized_keys" && chmod 600 "/home/$SSH_USER/.ssh/authorized_keys"`

func instanceRootless() bool { return os.Getenv("CLOUD_ROOTLESS") == "true" }

func instanceImage(ctx context.Context, name string) (string, error) {
	if name != "debian-12" {
		return "", fmt.Errorf("unknown instance image %s", name)
	}
	libraryMu.Lock()
	defer libraryMu.Unlock()
	return resolveBaseImage(ctx, kic.BaseImage)
}

func (i *Infra) instanceContainers(ctx context.Context, client *dockerclient.Client) ([]container.Summary, error) {
	args := filters.NewArgs(filters.Arg("label", instanceLabel), filters.Arg("label", instanceNetLabel+"="+i.network))
	return client.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
}

// CreateInstance starts a system container and returns once SSH accepts the student key.
func (i *Infra) CreateInstance(ctx context.Context, spec infra.InstanceSpec) (infra.Instance, error) {
	if i.network == "" {
		return infra.Instance{}, errors.New("standalone instances require CLOUD_DOCKER_NETWORK")
	}
	client, err := dockerClient()
	if err != nil {
		return infra.Instance{}, err
	}
	defer func() { _ = client.Close() }()
	ip, err := i.instanceSlot(ctx, client, spec)
	if err != nil {
		return infra.Instance{}, err
	}
	image, err := instanceImage(ctx, spec.Image)
	if err != nil {
		return infra.Instance{}, fmt.Errorf("prepare image: %w", err)
	}
	name := instancePrefix + spec.Name
	if _, err := client.VolumeCreate(ctx, volume.CreateOptions{Name: name, Labels: map[string]string{instanceLabel: spec.Name, instanceNetLabel: i.network}}); err != nil {
		return infra.Instance{}, fmt.Errorf("create volume: %w", err)
	}
	cfg, host, endpoints := instanceContainer(spec, image, i.network, ip, instanceRootless())
	created, err := client.ContainerCreate(ctx, cfg, host, endpoints, nil, name)
	if err != nil {
		return infra.Instance{}, fmt.Errorf("create container: %w", err)
	}
	for _, warning := range created.Warnings {
		slog.Warn("container create warning", "instance", spec.Name, "warning", warning)
	}
	key, err := i.bootInstance(ctx, client, spec, ip.String(), host.Resources)
	if err != nil {
		return infra.Instance{}, err
	}
	return infra.Instance{Name: spec.Name, IP: ip.String(), HostKey: key, Running: true}, nil
}

// instanceSlot runs the cheap host checks before any image download and returns a free address.
func (i *Infra) instanceSlot(ctx context.Context, client *dockerclient.Client, spec infra.InstanceSpec) (netip.Addr, error) {
	info, err := client.Info(ctx)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("inspect Docker host: %w", err)
	}
	if spec.Shape.CPUs > info.NCPU || int64(spec.Shape.MemoryMiB)*1024*1024 > info.MemTotal {
		return netip.Addr{}, errors.New("machine shape exceeds Docker host resources")
	}
	existing, err := i.instanceContainers(ctx, client)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("list instances: %w", err)
	}
	reserved, err := reservedAddresses(existing, spec.Name)
	if err != nil {
		return netip.Addr{}, err
	}
	// A volume left by a lost record would hand the old /var to a new instance.
	name := instancePrefix + spec.Name
	if _, err := client.VolumeInspect(ctx, name); err == nil {
		return netip.Addr{}, fmt.Errorf("volume %s already exists; remove it or choose another name", name)
	} else if !cerrdefs.IsNotFound(err) {
		return netip.Addr{}, fmt.Errorf("inspect volume: %w", err)
	}
	n, err := client.NetworkInspect(ctx, i.network, network.InspectOptions{})
	if err != nil {
		return netip.Addr{}, fmt.Errorf("inspect lab network: %w", err)
	}
	return instanceAddress(n, reserved)
}

// bootInstance starts a created container, prepares it for the student and returns its host key.
func (i *Infra) bootInstance(ctx context.Context, client *dockerclient.Client, spec infra.InstanceSpec, ip string, limits container.Resources) (string, error) {
	name := instancePrefix + spec.Name
	if err := client.ContainerStart(ctx, name, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start container: %w", err)
	}
	if err := verifyInstanceLimits(ctx, client, name, limits); err != nil {
		return "", err
	}
	if instanceRootless() {
		if err := restoreRootlessMAC(ctx, name, i.network); err != nil {
			return "", err
		}
	}
	if err := waitSSH(ctx, net.JoinHostPort(ip, "22"), containerAlive(client, name)); err != nil {
		return "", err
	}
	if err := execRoot(ctx, client, name, []string{"SSH_USER=" + spec.SSHUser, "AUTHORIZED_KEY=" + spec.AuthorizedKey}, prepareInstance); err != nil {
		return "", fmt.Errorf("install SSH key: %w", err)
	}
	return readHostKey(ctx, client, name)
}

func verifyInstanceLimits(ctx context.Context, client *dockerclient.Client, name string, want container.Resources) error {
	observed, err := client.ContainerInspect(ctx, name)
	if err != nil {
		return err
	}
	got := observed.HostConfig
	if got == nil || got.CPUQuota != want.CPUQuota || got.Memory != want.Memory || got.MemorySwap != want.MemorySwap || got.PidsLimit == nil || *got.PidsLimit != *want.PidsLimit {
		return errors.New("requested CPU/memory limits were not applied; check cgroups delegation")
	}
	return nil
}

// containerAlive fails once the container stops, so a crashed boot is not reported as an SSH timeout.
func containerAlive(client *dockerclient.Client, name string) func(context.Context) error {
	return func(ctx context.Context) error {
		observed, err := client.ContainerInspect(ctx, name)
		if err != nil {
			return fmt.Errorf("inspect container: %w", err)
		}
		if observed.State == nil || !observed.State.Running {
			code := 0
			if observed.State != nil {
				code = observed.State.ExitCode
			}
			return fmt.Errorf("container %s exited with code %d", name, code)
		}
		return nil
	}
}

func waitSSH(ctx context.Context, address string, alive func(context.Context) error) error {
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := alive(ctx); err != nil {
			return false, err
		}
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return false, nil // keep polling until sshd listens
		}
		_ = conn.Close()
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("wait for SSH on %s: %w", address, err)
	}
	return nil
}

func readHostKey(ctx context.Context, client *dockerclient.Client, name string) (string, error) {
	stream, _, err := client.CopyFromContainer(ctx, name, "/etc/ssh/ssh_host_ed25519_key.pub")
	if err != nil {
		return "", fmt.Errorf("read SSH host key: %w", err)
	}
	defer func() { _ = stream.Close() }()
	archive := tar.NewReader(stream)
	if _, err := archive.Next(); err != nil {
		return "", fmt.Errorf("read SSH host key: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(archive, 4096))
	if err != nil {
		return "", fmt.Errorf("read SSH host key: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", errors.New("read SSH host key: empty file")
	}
	return key, nil
}

// StartInstance boots a stopped instance and returns once SSH listens.
func (i *Infra) StartInstance(ctx context.Context, name string) (infra.Instance, error) {
	client, err := dockerClient()
	if err != nil {
		return infra.Instance{}, err
	}
	defer func() { _ = client.Close() }()
	id := instancePrefix + name
	if err := client.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return infra.Instance{}, fmt.Errorf("start container: %w", err)
	}
	observed, err := client.ContainerInspect(ctx, id)
	if err != nil {
		return infra.Instance{}, err
	}
	ip := observed.Config.Labels[instanceIPLabel]
	if err := waitSSH(ctx, net.JoinHostPort(ip, "22"), containerAlive(client, id)); err != nil {
		return infra.Instance{}, err
	}
	key, err := readHostKey(ctx, client, id)
	if err != nil {
		return infra.Instance{}, err
	}
	return infra.Instance{Name: name, IP: ip, HostKey: key, Running: true}, nil
}

// StopInstance halts systemd through the container's stop signal.
func (i *Infra) StopInstance(ctx context.Context, name string) error {
	client, err := dockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	timeout := 30
	return client.ContainerStop(ctx, instancePrefix+name, container.StopOptions{Timeout: &timeout})
}

// DeleteInstance removes the container, then its /var volume.
func (i *Infra) DeleteInstance(ctx context.Context, name string) error {
	client, err := dockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	id := instancePrefix + name
	if err := client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove container: %w", err)
	}
	if err := client.VolumeRemove(ctx, id, true); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove volume: %w", err)
	}
	return nil
}

// Instances lists the instance containers on the lab network.
func (i *Infra) Instances(ctx context.Context) ([]infra.Instance, error) {
	client, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	rows, err := i.instanceContainers(ctx, client)
	if err != nil {
		return nil, err
	}
	out := make([]infra.Instance, 0, len(rows))
	for _, c := range rows {
		out = append(out, infra.Instance{Name: c.Labels[instanceLabel], IP: c.Labels[instanceIPLabel], Running: c.State == "running"})
	}
	return out, nil
}
