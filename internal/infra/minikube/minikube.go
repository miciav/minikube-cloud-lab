// Package minikube implements the infrastructure boundary using the minikube library.
package minikube

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/viper"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	"k8s.io/minikube/pkg/drivers/kic"
	"k8s.io/minikube/pkg/drivers/kic/oci"
	"k8s.io/minikube/pkg/libmachine"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/constants"
	"k8s.io/minikube/pkg/minikube/kubeconfig"
	"k8s.io/minikube/pkg/minikube/localpath"
	"k8s.io/minikube/pkg/minikube/machine"
	"k8s.io/minikube/pkg/minikube/out"
	"k8s.io/minikube/pkg/minikube/out/register"

	// Register the in-process Docker driver.
	_ "k8s.io/minikube/pkg/minikube/registry/drvs/docker"
	"k8s.io/minikube/pkg/minikube/run"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

// minikube uses process-wide viper settings, so all library access is serialized.
var (
	libraryMu sync.Mutex
	options   = &run.CommandOptions{NonInteractive: true}
)

type (
	// Infra manages a dedicated minikube home.
	Infra     struct{ home, network, providerIP string }
	logWriter struct{}
)

func (logWriter) Write(p []byte) (int, error) {
	if s := strings.TrimSpace(string(p)); s != "" {
		slog.Info(s, "component", "infrastructure")
	}
	return len(p), nil
}
func (logWriter) Fd() uintptr { return os.Stderr.Fd() }

// New initializes the dedicated state directory and library defaults.
func New(dataDir string) (*Infra, error) {
	network, providerIP := os.Getenv("CLOUD_DOCKER_NETWORK"), os.Getenv("CLOUD_DOCKER_IP")
	if (network == "") != (providerIP == "") {
		return nil, fmt.Errorf("CLOUD_DOCKER_NETWORK and CLOUD_DOCKER_IP must be set together")
	}
	if network != "" {
		address, err := netip.ParseAddr(providerIP)
		validName, _ := regexp.MatchString(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`, network)
		if err != nil || !address.Is4() || !address.IsPrivate() || address.As4()[3] != 254 || !validName {
			return nil, fmt.Errorf("bridge mode requires a valid Docker network name and a private IPv4 cloud address ending in .254")
		}
	}
	home, err := filepath.Abs(filepath.Join(dataDir, "minikube"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	libraryMu.Lock()
	defer libraryMu.Unlock()
	if err := os.Setenv("MINIKUBE_HOME", home); err != nil {
		return nil, err
	}
	for _, dir := range []string{"certs", "addons", "machines", "cache", "profiles", "config", "files", "logs"} {
		if err := os.MkdirAll(filepath.Join(localpath.MiniPath(), dir), 0o700); err != nil {
			return nil, err
		}
	}
	klog.SetLogger(logr.FromSlogHandler(slog.Default().Handler()).V(1))
	out.SetSilent(true)
	register.Reg.SetStep(register.InitialSetup)
	machine.StartDriver(options)
	out.SetOutFile(logWriter{})
	out.SetErrFile(logWriter{})
	if err := os.Setenv("KUBECONFIG", filepath.Join(home, "kubeconfig")); err != nil {
		return nil, err
	}
	viper.Set("delete-on-failure", true)
	viper.Set("bootstrapper", "kubeadm")
	viper.Set("wait-timeout", 6*time.Minute)
	return &Infra{home: home, network: network, providerIP: providerIP}, nil
}

func (i *Infra) selectCluster(name string) error {
	if err := os.Setenv("MINIKUBE_HOME", i.home); err != nil {
		return err
	}
	viper.Set(config.ProfileName, name)
	return nil
}

// CreateCluster provisions and boots the control plane.
func (i *Infra) CreateCluster(ctx context.Context, spec infra.ClusterSpec) error {
	libraryMu.Lock()
	defer libraryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.selectCluster(spec.Name); err != nil {
		return err
	}
	host, err := oci.DaemonInfo("docker")
	if err != nil {
		return err
	}
	memory := int64(spec.Shape.MemoryMiB) * 1024 * 1024
	if spec.Shape.CPUs > host.CPUs || memory > host.TotalMemory {
		return fmt.Errorf("machine shape exceeds Docker host resources")
	}
	// Docker quotas do not hide host capacity from kubelet; reserve the excess for scheduling.
	reserved := fmt.Sprintf("cpu=%d,memory=%d", host.CPUs-spec.Shape.CPUs, host.TotalMemory-memory)
	labels := make([]string, 0, len(spec.NodeLabels))
	for k, v := range spec.NodeLabels {
		labels = append(labels, k+"="+v)
	}
	sort.Strings(labels)
	n := config.Node{Name: "", Port: 8443, KubernetesVersion: spec.KubernetesVersion, ContainerRuntime: "docker", ControlPlane: true, Worker: true}
	cc := &config.ClusterConfig{
		Name: spec.Name, Driver: "docker", Network: i.network, Memory: spec.Shape.MemoryMiB, CPUs: spec.Shape.CPUs, DiskSize: 20000,
		KicBaseImage: kic.BaseImage, APIServerPort: 8443, StartHostTimeout: 6 * time.Minute, CertExpiration: constants.DefaultCertExpiration,
		KeepContext: true, EmbedCerts: true, MultiNodeRequested: true, ListenAddress: "127.0.0.1", Nodes: []config.Node{n},
		KubernetesConfig: config.KubernetesConfig{KubernetesVersion: spec.KubernetesVersion, ClusterName: spec.Name, Namespace: "default", APIServerName: "minikubeCA", DNSDomain: "cluster.local", ContainerRuntime: "docker", ServiceCIDR: constants.DefaultServiceCIDR, ImageRepository: "registry.k8s.io", CNI: "flannel", NetworkPlugin: "cni", ExtraOptions: config.ExtraOptionSlice{{Component: "kubelet", Key: "node-labels", Value: strings.Join(labels, ",")}, {Component: "kubelet", Key: "kube-reserved", Value: reserved}}},
	}
	m, h, r, err := provisionNode(ctx, cc, &cc.Nodes[0])
	if m != nil {
		defer func() {
			if err := m.Close(); err != nil {
				slog.Error("close infrastructure client", "error", err)
			}
		}()
	}
	if err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if err := setMachineResources(ctx, spec.Name, spec.Shape); err != nil {
			return err
		}
	}
	settings, err := startNode(ctx, cc, &cc.Nodes[0], m, h, r, i.providerIP)
	if err != nil {
		return err
	}
	settings.EmbedCerts = true
	settings.ClusterServerAddress, err = forwardedAPIServer(ctx, spec.Name)
	if err != nil {
		return err
	}
	cfg := clientapi.NewConfig()
	if err := kubeconfig.PopulateFromSettings(settings, cfg); err != nil {
		return err
	}
	cfg.CurrentContext = spec.Name
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(i.home, spec.Name+".kubeconfig"), data, 0o600); err != nil {
		return err
	}
	if err := enableYAKD(ctx, cc); err != nil {
		return fmt.Errorf("enable YAKD addon: %w", err)
	}
	return nil
}

// DeleteCluster removes machines and their dedicated resources.
func (i *Infra) DeleteCluster(ctx context.Context, name string) error {
	libraryMu.Lock()
	defer libraryMu.Unlock()
	if err := i.deleteServiceExposure(ctx, name); err != nil {
		return err
	}
	if err := i.selectCluster(name); err != nil {
		return err
	}
	if !config.ProfileExists(name) {
		return nil
	}
	p, err := config.LoadProfile(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	api, err := machine.NewAPIClient(options)
	if err != nil {
		return err
	}
	defer func() {
		if err := api.Close(); err != nil {
			slog.Error("close infrastructure client", "error", err)
		}
	}()
	var errs []error
	names := []string{name}
	for _, n := range p.Config.Nodes {
		machineName := config.MachineName(*p.Config, n)
		if machineName != name {
			names = append(names, machineName)
		}
		if err := removeMachine(ctx, api, machineName); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for _, machineName := range names {
		label := oci.ProfileLabelKey + "=" + machineName
		errs = append(errs, oci.DeleteContainersByLabel("docker", label)...)
		errs = append(errs, oci.DeleteAllVolumesByLabel(ctx, "docker", label)...)
		errs = append(errs, oci.DeleteKICNetworksByLabel("docker", label)...)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if err := config.DeleteProfile(name); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(i.home, name+".kubeconfig")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func removeMachine(ctx context.Context, api libmachine.API, name string) error {
	exists, err := oci.ContainerExists("docker", name)
	if err != nil {
		return err
	}
	if exists {
		known, err := api.Exists(name)
		if err != nil {
			return err
		}
		if known {
			err = machine.DeleteHost(api, name)
		} else {
			err = oci.DeleteContainer(ctx, "docker", name)
		}
		if err != nil {
			return err
		}
	}
	// DeleteHost can swallow a driver error; observe absence before discarding ownership.
	remaining, err := oci.ContainerExists("docker", name)
	if err != nil {
		return err
	}
	if remaining {
		return fmt.Errorf("machine %s still exists after deletion", name)
	}
	return api.Remove(name)
}

// AddMachine joins a worker using the cluster shape.
func (i *Infra) AddMachine(ctx context.Context, name string) (infra.Machine, error) {
	libraryMu.Lock()
	defer libraryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return infra.Machine{}, err
	}
	if err := i.selectCluster(name); err != nil {
		return infra.Machine{}, err
	}
	p, err := config.LoadProfile(name)
	if err != nil {
		return infra.Machine{}, err
	}
	cc := p.Config
	index := 2
	for {
		candidate := fmt.Sprintf("m%02d", index)
		found := false
		for _, existing := range cc.Nodes {
			if existing.Name == candidate {
				found = true
				break
			}
		}
		if !found {
			break
		}
		index++
	}
	n := config.Node{Name: fmt.Sprintf("m%02d", index), KubernetesVersion: cc.KubernetesConfig.KubernetesVersion, ContainerRuntime: cc.KubernetesConfig.ContainerRuntime, Worker: true}
	m, h, r, err := provisionNode(ctx, cc, &n)
	if m != nil {
		defer func() { _ = m.Close() }()
	}
	partial := infra.Machine{Name: config.MachineName(*cc, n)}
	if err != nil {
		return partial, err
	}
	if runtime.GOOS == "linux" {
		if err := setMachineResources(ctx, partial.Name, infra.MachineShape{CPUs: cc.CPUs, MemoryMiB: cc.Memory}); err != nil {
			return partial, err
		}
	}
	if _, err := startNode(ctx, cc, &n, m, h, r, i.providerIP); err != nil {
		return partial, err
	}

	return infra.Machine{Name: config.MachineName(*cc, n), Running: true}, nil
}

// DeleteMachine drains and removes a worker.
func (i *Infra) DeleteMachine(ctx context.Context, name, m string) error {
	libraryMu.Lock()
	defer libraryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.selectCluster(name); err != nil {
		return err
	}
	p, err := config.LoadProfile(name)
	if err != nil {
		return err
	}
	return i.removeWorker(ctx, p.Config, m)
}

// Machines reports the machines observed by the Docker driver.
func (i *Infra) Machines(ctx context.Context, name string) ([]infra.Machine, error) {
	libraryMu.Lock()
	defer libraryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := i.selectCluster(name); err != nil {
		return nil, err
	}
	if !config.ProfileExists(name) {
		return []infra.Machine{}, nil
	}
	p, err := config.LoadProfile(name)
	if err != nil {
		return nil, err
	}
	result := make([]infra.Machine, 0, len(p.Config.Nodes))
	for _, n := range p.Config.Nodes {
		machineName := config.MachineName(*p.Config, n)
		exists, err := oci.ContainerExists("docker", machineName)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		running, err := oci.ContainerRunning("docker", machineName)
		if err != nil {
			return nil, err
		}
		result = append(result, infra.Machine{Name: machineName, ControlPlane: n.ControlPlane, Running: running})
	}
	return result, nil
}

// Kubeconfig returns the cluster identity with embedded certificates.
func (i *Infra) Kubeconfig(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(i.home, name+".kubeconfig")) // #nosec G304 -- names are validated by the cloud service before crossing this boundary.
}
