package minikube

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	dockerclient "github.com/docker/docker/client"
	"k8s.io/minikube/pkg/minikube/config"
)

// containerdHosts is containerd's registry config_path in the kicbase image. containerd ignores
// CIDR entries there, so each registry needs its own host directory; it re-reads them per pull.
const containerdHosts = "/etc/containerd/certs.d"

func registryHostsDir(name string) string {
	return fmt.Sprintf("%s/%s.registry:5000", containerdHosts, name)
}

// registryHostsTOML lets containerd pull from the registry over HTTP.
func registryHostsTOML(name string) []byte {
	return fmt.Appendf(nil, `server = "http://%[1]s.registry:5000"

[host."http://%[1]s.registry:5000"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
`, name)
}

func writeRegistryHosts(ctx context.Context, client *dockerclient.Client, node string, names []string) {
	files := make(map[string][]byte, len(names))
	for _, name := range names {
		files[registryHostsDir(name)[1:]+"/hosts.toml"] = registryHostsTOML(name)
	}
	if err := copyFiles(ctx, client, node, files); err != nil {
		// A later node creation or cloud restart writes the file again.
		slog.Warn("node cannot pull from registries by name", "node", node, "registries", names, "error", err)
	}
}

// containerdNodes lists the node containers of clusters running containerd. Docker-runtime
// nodes already trust the lab subnet. The cloud serializes infrastructure mutations, so no
// profile is written meanwhile.
func (i *Infra) containerdNodes() []string {
	entries, err := os.ReadDir(filepath.Join(i.home, "profiles"))
	if err != nil && !os.IsNotExist(err) {
		slog.Warn("cannot list clusters for registry hosts", "error", err)
	}
	var nodes []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p, err := config.LoadProfile(entry.Name(), i.home)
		if err != nil {
			slog.Warn("cannot load cluster for registry hosts", "cluster", entry.Name(), "error", err)
			continue
		}
		if p.Config.KubernetesConfig.ContainerRuntime != "containerd" {
			continue
		}
		for _, n := range p.Config.Nodes {
			nodes = append(nodes, config.MachineName(*p.Config, n))
		}
	}
	return nodes
}

// addRegistryHosts trusts a running registry on every containerd node.
func (i *Infra) addRegistryHosts(ctx context.Context, client *dockerclient.Client, name string) {
	for _, node := range i.containerdNodes() {
		writeRegistryHosts(ctx, client, node, []string{name})
	}
}

// removeRegistryHosts forgets a deleted registry on every containerd node. name is a DNS-1123
// label checked by registryDir and passed as an argument, never through a shell.
func (i *Infra) removeRegistryHosts(ctx context.Context, client *dockerclient.Client, name string) {
	for _, node := range i.containerdNodes() {
		if err := execRootWithin(ctx, client, node, nil, []string{"rm", "-rf", "--", registryHostsDir(name)}, time.Minute); err != nil {
			slog.Warn("cannot remove registry hosts", "node", node, "registry", name, "error", err)
		}
	}
}

// trustRegistries writes every running registry into a new containerd node.
func (i *Infra) trustRegistries(ctx context.Context, cc *config.ClusterConfig, node string) {
	if i.network == "" || cc.KubernetesConfig.ContainerRuntime != "containerd" {
		return
	}
	client, err := dockerClient()
	if err != nil {
		slog.Warn("cannot trust registries", "node", node, "error", err)
		return
	}
	defer func() { _ = client.Close() }()
	rows, err := i.registryContainers(ctx, client)
	if err != nil {
		slog.Warn("cannot trust registries", "node", node, "error", err)
		return
	}
	var names []string
	for _, c := range rows {
		if c.State == "running" {
			names = append(names, c.Labels[registryLabel])
		}
	}
	if len(names) > 0 {
		writeRegistryHosts(ctx, client, node, names)
	}
}
