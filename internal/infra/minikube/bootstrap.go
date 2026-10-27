package minikube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/minikube/pkg/drivers/kic"
	"k8s.io/minikube/pkg/kapi"
	"k8s.io/minikube/pkg/libmachine"
	"k8s.io/minikube/pkg/libmachine/host"
	"k8s.io/minikube/pkg/minikube/bootstrapper"
	"k8s.io/minikube/pkg/minikube/bootstrapper/bsutil/kverify"
	"k8s.io/minikube/pkg/minikube/cluster"
	"k8s.io/minikube/pkg/minikube/cni"
	"k8s.io/minikube/pkg/minikube/command"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/constants"
	"k8s.io/minikube/pkg/minikube/cruntime"
	"k8s.io/minikube/pkg/minikube/detect"
	"k8s.io/minikube/pkg/minikube/download"
	"k8s.io/minikube/pkg/minikube/driver"
	"k8s.io/minikube/pkg/minikube/kubeconfig"
	"k8s.io/minikube/pkg/minikube/localpath"
	"k8s.io/minikube/pkg/minikube/machine"
	"k8s.io/minikube/pkg/minikube/registry"
	"k8s.io/minikube/pkg/minikube/sysinit"
	"k8s.io/minikube/pkg/util"
)

// The node package wraps these primitives with CLI fatal exits, unsuitable for a daemon.
func provisionNode(ctx context.Context, cc *config.ClusterConfig, n *config.Node) (libmachine.API, *host.Host, command.Runner, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if cc.Driver != "docker" || cc.StaticIP != "" {
		return nil, nil, nil, fmt.Errorf("only Docker with automatic node addresses is supported")
	}
	if err := checkBridgeNetwork(cc.Network); err != nil {
		return nil, nil, nil, err
	}
	if err := prepareBaseImage(ctx, cc); err != nil {
		return nil, nil, nil, err
	}
	if err := config.SaveNode(cc, n); err != nil {
		return nil, nil, nil, err
	}
	api, err := machine.NewAPIClient(options)
	if err != nil {
		return nil, nil, nil, err
	}
	definition, err := registry.Driver("docker").Config(*cc, *n)
	if err != nil {
		return api, nil, nil, err
	}
	data, err := json.Marshal(definition)
	if err != nil {
		return api, nil, nil, err
	}
	h, err := api.NewHost("docker", data)
	if err != nil {
		return api, nil, nil, err
	}
	h.HostOptions.AuthOptions.CertDir = localpath.MiniPath()
	h.HostOptions.AuthOptions.StorePath = localpath.MiniPath()
	if cc.Network != "" {
		h.Driver = privateSSHDriver{h.Driver}
	}
	if err := api.Create(h); err != nil {
		return api, h, nil, err
	}
	r, err := machine.CommandRunner(h)
	if err != nil {
		return api, h, nil, err
	}
	n.IP, err = h.Driver.GetIP()
	if err != nil {
		return api, h, r, err
	}
	return api, h, r, config.SaveNode(cc, n)
}

func prepareBaseImage(ctx context.Context, cc *config.ClusterConfig) error {
	var failures []error
	for _, img := range append([]string{cc.KicBaseImage}, kic.FallbackImages...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if download.ImageExistsInDaemon(img) {
			cc.KicBaseImage = img
			return nil
		}
		if err := download.ImageToCache(img); err != nil {
			failures = append(failures, err)
			continue
		}
		loaded, err := download.CacheToDaemon(img)
		if err == nil {
			cc.KicBaseImage = loaded
			return nil
		}
		failures = append(failures, err)
	}
	return fmt.Errorf("prepare node image: %w", errors.Join(failures...))
}

func configureRuntime(ctx context.Context, cc config.ClusterConfig, r cruntime.CommandRunner) (cruntime.Manager, error) {
	version, err := util.ParseKubernetesVersion(cc.KubernetesConfig.KubernetesVersion)
	if err != nil {
		return nil, err
	}
	if err := cni.ConfigureLoopbackCNI(r, false); err != nil {
		return nil, err
	}
	if err := cni.ConfigureDefaultBridgeCNIs(r, cc.KubernetesConfig.NetworkPlugin); err != nil {
		return nil, err
	}
	cfg := cruntime.Config{Type: "containerd", Runner: r, ImageRepository: cc.KubernetesConfig.ImageRepository, KubernetesVersion: version}
	containerd, err := cruntime.New(cfg)
	if err != nil {
		return nil, err
	}
	if err := containerd.Enable(false, detect.CgroupDriver(), false); err != nil {
		return nil, err
	}
	cfg.Type = "docker"
	cfg.NetworkPlugin = cc.KubernetesConfig.NetworkPlugin
	cr, err := cruntime.New(cfg)
	if err != nil {
		return nil, err
	}
	if err := cr.Enable(true, detect.CgroupDriver(), false); err != nil {
		return nil, err
	}
	err = wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(context.Context) (bool, error) { _, err := cr.Version(); return err == nil, nil })
	return cr, err
}

func startNode(ctx context.Context, cc *config.ClusterConfig, n *config.Node, api libmachine.API, h *host.Host, r command.Runner, providerIP string) (*kubeconfig.Settings, error) {
	cr, err := configureRuntime(ctx, *cc, r)
	if err != nil {
		return nil, err
	}
	ip, err := providerAddress(h, cc.Name, providerIP)
	if err != nil {
		return nil, err
	}
	if err := machine.AddHostAlias(r, constants.HostAlias, ip); err != nil {
		return nil, err
	}
	bs, err := cluster.Bootstrapper(api, "kubeadm", *cc, r)
	if err != nil {
		return nil, err
	}
	if !n.ControlPlane {
		if err := joinWorker(cc, n, api, bs, cr); err != nil {
			return nil, err
		}
		return nil, verifyNodes(ctx, cc.Name, r)
	}
	settings, err := clusterSettings(cc, n, h, providerIP != "")
	if err != nil {
		return nil, err
	}
	if err := bs.UpdateCluster(*cc); err != nil {
		return nil, err
	}
	if err := bs.SetupCerts(*cc, *n, r); err != nil {
		return nil, err
	}
	if err := bs.StartCluster(*cc, options); err != nil {
		return nil, err
	}
	if err := kubeconfig.Update(settings); err != nil {
		return nil, err
	}
	if err := verifyNodes(ctx, cc.Name, r); err != nil {
		return nil, err
	}
	if err := configureClusterDNS(ctx, cc.Name, ip.String()); err != nil {
		return nil, err
	}
	return settings, nil
}

func providerAddress(h *host.Host, name, providerIP string) (net.IP, error) {
	if providerIP != "" {
		return net.ParseIP(providerIP), nil
	}
	return cluster.HostIP(h, name)
}

// Keep minikube's default pressure check without its host-endpoint override.
func verifyNodes(ctx context.Context, name string, r command.Runner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sysinit.New(r).Start("kubelet"); err != nil {
		slog.Warn("ensure kubelet is started", "error", err)
	}
	client, err := kapi.Client(name)
	if err != nil {
		return err
	}
	return kverify.NodePressure(client)
}

func joinWorker(cc *config.ClusterConfig, n *config.Node, api libmachine.API, bs bootstrapper.Bootstrapper, cr cruntime.Manager) error {
	cp, err := config.ControlPlane(*cc)
	if err != nil {
		return err
	}
	cpHost, err := machine.LoadHost(api, config.MachineName(*cc, cp))
	if err != nil {
		return err
	}
	if cc.Network != "" {
		cpHost.Driver = privateSSHDriver{cpHost.Driver}
	}
	cpRunner, err := machine.CommandRunner(cpHost)
	if err != nil {
		return err
	}
	if err := bs.SetupCerts(*cc, *n, cpRunner); err != nil {
		return err
	}
	if err := bs.UpdateNode(*cc, *n, cr); err != nil {
		return err
	}
	cpBootstrapper, err := cluster.Bootstrapper(api, "kubeadm", *cc, cpRunner)
	if err != nil {
		return err
	}
	token, err := cpBootstrapper.GenerateToken(*cc)
	if err != nil {
		return err
	}
	if err := bs.JoinCluster(*cc, *n, token); err != nil {
		return err
	}
	if err := cpBootstrapper.LabelAndUntaintNode(*cc, *n); err != nil {
		return err
	}
	return nil
}

func clusterSettings(cc *config.ClusterConfig, n *config.Node, h *host.Host, internal bool) (*kubeconfig.Settings, error) {
	ip, port := n.IP, n.Port
	if !internal {
		var err error
		ip, _, port, err = driver.ControlPlaneEndpoint(cc, n, h.DriverName)
		if err != nil {
			return nil, err
		}
	}
	s := &kubeconfig.Settings{ClusterName: cc.Name, Namespace: "default", ClusterServerAddress: "https://" + net.JoinHostPort(ip, strconv.Itoa(port)), ClientCertificate: localpath.ClientCert(cc.Name), ClientKey: localpath.ClientKey(cc.Name), CertificateAuthority: localpath.CACert(), KeepContext: false, EmbedCerts: true}
	s.SetPath(kubeconfig.PathFromEnv())
	return s, nil
}

func configureClusterDNS(ctx context.Context, name, ip string) error {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig.PathFromEnv()}, &clientcmd.ConfigOverrides{CurrentContext: name}).ClientConfig()
	if err != nil {
		return err
	}
	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	cm, err := c.CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
	if err != nil {
		return err
	}
	core := cm.Data["Corefile"]
	if !strings.Contains(core, constants.HostAlias) {
		switch {
		case strings.Contains(core, "hosts {"):
			cm.Data["Corefile"] = strings.Replace(core, "hosts {", fmt.Sprintf("hosts {\n %s %s", ip, constants.HostAlias), 1)
		case strings.Contains(core, "forward ."):
			cm.Data["Corefile"] = strings.Replace(core, "forward .", fmt.Sprintf("hosts {\n %s %s\n fallthrough\n }\n forward .", ip, constants.HostAlias), 1)
		default:
			return fmt.Errorf("cluster %s: unsupported CoreDNS configuration", name)
		}
		if _, err := c.CoreV1().ConfigMaps("kube-system").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}
