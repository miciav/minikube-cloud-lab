package serviceexposure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	ownerLabel   = "minikube-cloud-lab.service-proxy"
	clusterLabel = "minikube-cloud-lab.cluster"
	uidLabel     = "minikube-cloud-lab.service-uid"
	hashLabel    = "minikube-cloud-lab.spec-hash"
	ipLabel      = "minikube-cloud-lab.ip"
	typeLabel    = "minikube-cloud-lab.service-type"
)

// ErrUnsupported identifies Service features outside the local TCP contract.
var ErrUnsupported = errors.New("unsupported service exposure")

// HostPort is the observed localhost endpoint for one Service port.
type HostPort struct {
	Name        string `json:"name"`
	ServicePort int32  `json:"servicePort"`
	NodePort    int32  `json:"nodePort"`
	HostPort    int32  `json:"hostPort"`
}

// Status contains actual Docker addresses, never guessed host ports.
type Status struct {
	IP    string
	Ports []HostPort
}

// Manager owns only labeled relay containers on the configured bridge.
type Manager struct {
	client         *dockerclient.Client
	network, image string
}

// NewManager reuses the configured bridge and an image already present in Docker.
func NewManager(client *dockerclient.Client, networkName, imageID string) *Manager {
	return &Manager{client: client, network: networkName, image: imageID}
}

// Ensure creates or reuses the relay described by a persisted Kubernetes Service.
func (m *Manager) Ensure(ctx context.Context, cluster string, svc *corev1.Service, controlPlaneIP string) (Status, error) {
	if cluster == "" || m.network == "" || m.image == "" || svc == nil || svc.UID == "" {
		return Status{}, fmt.Errorf("relay requires cluster, network, image and Service UID")
	}
	forwards, err := serviceForwards(svc, controlPlaneIP)
	if err != nil {
		return Status{}, err
	}
	n, err := m.client.NetworkInspect(ctx, m.network, network.InspectOptions{})
	if err != nil {
		return Status{}, err
	}
	prefix, err := relayPrefix(n, controlPlaneIP)
	if err != nil {
		return Status{}, err
	}
	payload, _ := json.Marshal(forwards)
	sum := sha256.Sum256(append([]byte(svc.Spec.Type+":"), payload...))
	digest := fmt.Sprintf("%x", sum)
	existing, err := m.owned(ctx, cluster, svc.UID)
	if err != nil {
		return Status{}, err
	}
	for _, c := range existing {
		if c.Labels[hashLabel] == digest {
			return m.activate(ctx, c.ID, svc)
		}
		if err := m.remove(ctx, c.ID); err != nil {
			return Status{}, err
		}
		delete(n.Containers, c.ID)
	}
	ip, err := freeRelayIP(n, prefix)
	if err != nil {
		return Status{}, err
	}
	ports := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, f := range forwards {
		port := nat.Port(strconv.Itoa(int(f.ListenPort)) + "/tcp")
		ports[port] = struct{}{}
		hostPort := ""
		if svc.Spec.Type == corev1.ServiceTypeNodePort {
			hostPort = strconv.Itoa(int(f.ListenPort))
		}
		bindings[port] = []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: hostPort}}
	}
	nameHash := sha256.Sum256([]byte(cluster + "/" + string(svc.UID)))
	created, err := m.client.ContainerCreate(ctx, &container.Config{
		Image: m.image, Entrypoint: []string{"cloud"}, Cmd: []string{"service-proxy", "--config-json", string(payload)}, ExposedPorts: ports,
		Labels: map[string]string{ownerLabel: "true", clusterLabel: cluster, uidLabel: string(svc.UID), hashLabel: digest, ipLabel: ip.String(), typeLabel: string(svc.Spec.Type)},
	}, &container.HostConfig{PortBindings: bindings, RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}, Sysctls: map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"}}, &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{m.network: {IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: ip.String()}}}}, nil, fmt.Sprintf("mcl-svc-%x", nameHash[:10]))
	if err != nil {
		return Status{}, err
	}
	return m.activate(ctx, created.ID, svc)
}

func serviceForwards(svc *corev1.Service, target string) ([]Forward, error) {
	if svc.Spec.Type != corev1.ServiceTypeNodePort && svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return nil, fmt.Errorf("%w: only NodePort and LoadBalancer", ErrUnsupported)
	}
	if svc.Spec.ExternalTrafficPolicy != "" && svc.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyCluster {
		return nil, fmt.Errorf("%w: externalTrafficPolicy must be Cluster", ErrUnsupported)
	}
	if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
		if svc.Spec.LoadBalancerClass != nil && *svc.Spec.LoadBalancerClass != "" {
			return nil, fmt.Errorf("%w: custom loadBalancerClass", ErrUnsupported)
		}
		if svc.Spec.AllocateLoadBalancerNodePorts != nil && !*svc.Spec.AllocateLoadBalancerNodePorts {
			return nil, fmt.Errorf("%w: LoadBalancer requires allocated NodePorts", ErrUnsupported)
		}
	}
	forwards := make([]Forward, 0, len(svc.Spec.Ports))
	for _, port := range svc.Spec.Ports {
		if port.Protocol != "" && port.Protocol != corev1.ProtocolTCP {
			return nil, fmt.Errorf("%w: only TCP ports are supported", ErrUnsupported)
		}
		listen := port.NodePort
		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			listen = port.Port
		}
		forwards = append(forwards, Forward{ListenPort: listen, Target: net.JoinHostPort(target, strconv.Itoa(int(port.NodePort)))})
	}
	if err := validateForwards(forwards); err != nil {
		return nil, err
	}
	return forwards, nil
}

func relayPrefix(n network.Inspect, target string) (netip.Prefix, error) {
	if n.Driver != "bridge" || len(n.IPAM.Config) != 1 {
		return netip.Prefix{}, fmt.Errorf("service exposure requires one IPv4 /23 bridge subnet")
	}
	p, err := netip.ParsePrefix(n.IPAM.Config[0].Subnet)
	if err != nil || !p.Addr().Is4() || !p.Addr().IsPrivate() || p.Bits() != 23 || p != p.Masked() || n.IPAM.Config[0].Gateway != p.Addr().Next().String() {
		return netip.Prefix{}, fmt.Errorf("service exposure requires an aligned private /23 and explicit first-host gateway; recreate the lab network")
	}
	node, err := netip.ParseAddr(target)
	firstHalf := netip.PrefixFrom(p.Addr(), 24)
	if err != nil || !firstHalf.Contains(node) || node.As4()[3] < 2 || node.As4()[3] > 253 {
		return netip.Prefix{}, fmt.Errorf("control plane must be in the node half of the managed bridge")
	}
	base := p.Addr().As4()
	base[2]++
	return netip.PrefixFrom(netip.AddrFrom4(base), 24), nil
}

func freeRelayIP(n network.Inspect, p netip.Prefix) (netip.Addr, error) {
	used := map[netip.Addr]bool{}
	for _, endpoint := range n.Containers {
		if endpoint.IPv4Address == "" {
			continue
		}
		member, err := netip.ParsePrefix(endpoint.IPv4Address)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("invalid Docker network member address: %w", err)
		}
		used[member.Addr()] = true
	}
	address := p.Addr().Next()
	for i := 0; i < 254; i++ {
		if !used[address] {
			return address, nil
		}
		address = address.Next()
	}
	return netip.Addr{}, fmt.Errorf("service relay address pool exhausted")
}

func (m *Manager) activate(ctx context.Context, id string, svc *corev1.Service) (Status, error) {
	status, err := m.observe(ctx, id, svc)
	if err != nil {
		return Status{}, errors.Join(err, m.remove(ctx, id))
	}
	return status, nil
}

func (m *Manager) observe(ctx context.Context, id string, svc *corev1.Service) (Status, error) {
	observed, err := m.client.ContainerInspect(ctx, id)
	if err != nil {
		return Status{}, err
	}
	if observed.State == nil || !observed.State.Running {
		if err := m.client.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
			return Status{}, err
		}
		observed, err = m.client.ContainerInspect(ctx, id)
		if err != nil {
			return Status{}, err
		}
	}
	if observed.NetworkSettings == nil || observed.NetworkSettings.Networks[m.network] == nil {
		return Status{}, fmt.Errorf("relay is missing its managed network")
	}
	ip := observed.NetworkSettings.Networks[m.network].IPAddress
	if ip == "" || observed.Config == nil || observed.Config.Labels[ipLabel] != ip {
		return Status{}, fmt.Errorf("relay address differs from its managed allocation")
	}
	status := Status{IP: ip}
	for _, port := range svc.Spec.Ports {
		listen := port.NodePort
		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			listen = port.Port
		}
		bindings := observed.NetworkSettings.Ports[nat.Port(strconv.Itoa(int(listen))+"/tcp")]
		if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" {
			return Status{}, fmt.Errorf("relay port %d has no exclusive localhost binding", listen)
		}
		hostPort, err := strconv.ParseInt(bindings[0].HostPort, 10, 32)
		if err != nil || hostPort < 1 || hostPort > 65535 || (svc.Spec.Type == corev1.ServiceTypeNodePort && int32(hostPort) != port.NodePort) {
			return Status{}, fmt.Errorf("relay port %d has an invalid host binding", listen)
		}
		status.Ports = append(status.Ports, HostPort{Name: port.Name, ServicePort: port.Port, NodePort: port.NodePort, HostPort: int32(hostPort)})
	}
	return status, nil
}

func (m *Manager) owned(ctx context.Context, cluster string, uid types.UID) ([]container.Summary, error) {
	if cluster == "" {
		return nil, fmt.Errorf("cluster ownership is required")
	}
	args := filters.NewArgs(filters.Arg("label", ownerLabel+"=true"), filters.Arg("label", clusterLabel+"="+cluster))
	if uid != "" {
		args.Add("label", uidLabel+"="+string(uid))
	}
	rows, err := m.client.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, err
	}
	result := make([]container.Summary, 0, len(rows))
	for _, c := range rows {
		if c.Labels[ownerLabel] == "true" && c.Labels[clusterLabel] == cluster && (uid == "" || c.Labels[uidLabel] == string(uid)) {
			result = append(result, c)
		}
	}
	return result, nil
}

func (m *Manager) remove(ctx context.Context, id string) error {
	return m.client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

// Remove deletes only relay containers bearing the exact cluster and Service UID.
func (m *Manager) Remove(ctx context.Context, cluster string, uid types.UID) error {
	if uid == "" {
		return fmt.Errorf("service UID is required")
	}
	rows, err := m.owned(ctx, cluster, uid)
	if err != nil {
		return err
	}
	var failures []error
	for _, c := range rows {
		failures = append(failures, m.remove(ctx, c.ID))
	}
	return errors.Join(failures...)
}

// Prune requires a successful, complete Kubernetes list before its caller invokes it.
func (m *Manager) Prune(ctx context.Context, cluster string, keep []types.UID) error {
	rows, err := m.owned(ctx, cluster, "")
	if err != nil {
		return err
	}
	retained := make(map[string]bool, len(keep))
	for _, uid := range keep {
		retained[string(uid)] = true
	}
	var failures []error
	for _, c := range rows {
		if !retained[c.Labels[uidLabel]] {
			failures = append(failures, m.remove(ctx, c.ID))
		}
	}
	return errors.Join(failures...)
}

// DeleteCluster does not require a Kubernetes API or minikube profile.
func (m *Manager) DeleteCluster(ctx context.Context, cluster string) error {
	return m.Prune(ctx, cluster, nil)
}
