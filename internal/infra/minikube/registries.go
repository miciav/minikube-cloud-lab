package minikube

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"golang.org/x/crypto/bcrypt"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

const (
	registryPrefix = "mcl-registry-"
	registryLabel  = "minikube-cloud-lab.registry"
	// registryImage is the zot server proven by tests/registry-feasibility.sh.
	registryImage          = "ghcr.io/project-zot/zot:v2.1.23@sha256:9d3de83ac2099e6f4843045b4c9b65b7e8baf40f92d28f54906dee9d4d390fd7"
	registryMemory         = 256 << 20
	registryMount          = "/var/lib/registry"
	registryConfigPath     = "/etc/zot/config.json"
	registryManifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"
)

var _ infra.Registries = (*Infra)(nil)

// registryRoot is zot's storage directory. Rootful Docker mounts the registry's own volume
// subpath; rootless Podman mounts the whole volume, as for shared storage, and zot keeps
// each registry in its own directory.
func registryRoot(name string, rootless bool) string {
	if rootless {
		return registryMount + "/" + name
	}
	return registryMount
}

func registryContainer(spec infra.RegistrySpec, networkName, volumeName string, ip netip.Addr, rootless bool) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	cfg := &container.Config{
		Image: registryImage, Cmd: []string{"serve", registryConfigPath},
		Labels: map[string]string{registryLabel: spec.Name, instanceNetLabel: networkName, instanceIPLabel: ip.String()},
	}
	data := mount.Mount{Type: mount.TypeVolume, Source: volumeName, Target: registryMount, VolumeOptions: &mount.VolumeOptions{NoCopy: true}}
	if !rootless {
		data.VolumeOptions.Subpath = spec.Name
	}
	host := &container.HostConfig{
		Mounts:        []mount.Mount{data},
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources:     container.Resources{Memory: registryMemory, MemorySwap: registryMemory},
	}
	endpoint := &network.EndpointSettings{IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: ip.String()}, Aliases: []string{spec.Name + ".registry"}}
	return cfg, host, &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{networkName: endpoint}}
}

// registryConfig returns zot's config and htpasswd: push may read, create and update, pull may
// only read, anonymous access is denied.
func registryConfig(spec infra.RegistrySpec, root string) (config, htpasswd []byte, err error) {
	var users bytes.Buffer
	for _, user := range [][2]string{{spec.PushUser, spec.PushPassword}, {spec.PullUser, spec.PullPassword}} {
		// Cost 5 as htpasswd -B; the passwords are generated, not chosen.
		hash, err := bcrypt.GenerateFromPassword([]byte(user[1]), 5)
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(&users, "%s:%s\n", user[0], hash)
	}
	config, err = json.MarshalIndent(map[string]any{
		"distSpecVersion": "1.1.1",
		"storage":         map[string]any{"rootDirectory": root},
		"http": map[string]any{
			"address": "0.0.0.0", "port": "5000", "compat": []string{"docker2s2"},
			"auth": map[string]any{"htpasswd": map[string]any{"path": "/etc/zot/htpasswd"}},
			"accessControl": map[string]any{"repositories": map[string]any{"**": map[string]any{
				"policies": []map[string]any{
					{"users": []string{spec.PushUser}, "actions": []string{"read", "create", "update"}},
					{"users": []string{spec.PullUser}, "actions": []string{"read"}},
				},
				"defaultPolicy": []string{},
			}}},
		},
		"log": map[string]any{"level": "info"},
	}, "", "  ")
	return config, users.Bytes(), err
}

// registryDir is the registry's data directory as mounted in the cloud container.
func (i *Infra) registryDir(name string) string {
	return filepath.Join(filepath.Dir(i.home), "registries", name)
}

func (i *Infra) registryContainers(ctx context.Context, client *dockerclient.Client) ([]container.Summary, error) {
	args := filters.NewArgs(filters.Arg("label", registryLabel), filters.Arg("label", instanceNetLabel+"="+i.network))
	return client.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
}

// CreateRegistry replaces any container of the same name and starts zot on the existing data.
func (i *Infra) CreateRegistry(ctx context.Context, spec infra.RegistrySpec) (infra.RegistryInfo, error) {
	volumeName := os.Getenv("CLOUD_REGISTRY_VOLUME")
	if i.network == "" || volumeName == "" {
		return infra.RegistryInfo{}, errors.New("registries require CLOUD_DOCKER_NETWORK and CLOUD_REGISTRY_VOLUME")
	}
	client, err := dockerClient()
	if err != nil {
		return infra.RegistryInfo{}, err
	}
	defer func() { _ = client.Close() }()
	name := registryPrefix + spec.Name
	if err := client.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return infra.RegistryInfo{}, fmt.Errorf("remove old container: %w", err)
	}
	// Docker mounts only an existing subpath.
	if err := os.MkdirAll(i.registryDir(spec.Name), 0o700); err != nil {
		return infra.RegistryInfo{}, err
	}
	ip, err := i.registryAddress(ctx, client)
	if err != nil {
		return infra.RegistryInfo{}, err
	}
	if err := ensureImage(ctx, client, registryImage); err != nil {
		return infra.RegistryInfo{}, err
	}
	rootless := instanceRootless()
	cfg, host, endpoints := registryContainer(spec, i.network, volumeName, ip, rootless)
	created, err := client.ContainerCreate(ctx, cfg, host, endpoints, nil, name)
	if err != nil {
		return infra.RegistryInfo{}, fmt.Errorf("create container: %w", err)
	}
	config, htpasswd, err := registryConfig(spec, registryRoot(spec.Name, rootless))
	if err != nil {
		return infra.RegistryInfo{}, err
	}
	if err := copyFiles(ctx, client, created.ID, map[string][]byte{"etc/zot/config.json": config, "etc/zot/htpasswd": htpasswd}); err != nil {
		return infra.RegistryInfo{}, fmt.Errorf("copy registry config: %w", err)
	}
	if err := client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return infra.RegistryInfo{}, fmt.Errorf("start container: %w", err)
	}
	if err := waitRegistry(ctx, "http://"+ip.String()+":5000/v2/", containerAlive(client, name)); err != nil {
		return infra.RegistryInfo{}, err
	}
	return infra.RegistryInfo{Name: spec.Name, IP: ip.String(), Running: true}, nil
}

// registryAddress picks a .200–.252 address no instance (stopped included) or registry holds.
func (i *Infra) registryAddress(ctx context.Context, client *dockerclient.Client) (netip.Addr, error) {
	rows, err := i.labContainers(ctx, client)
	if err != nil {
		return netip.Addr{}, err
	}
	reserved, err := reservedAddresses(rows, "")
	if err != nil {
		return netip.Addr{}, err
	}
	n, err := client.NetworkInspect(ctx, i.network, network.InspectOptions{})
	if err != nil {
		return netip.Addr{}, fmt.Errorf("inspect lab network: %w", err)
	}
	return instanceAddress(n, reserved)
}

// copyFiles writes files into a created container before it starts; no bind mount needed on
// rootful or rootless runtimes.
func copyFiles(ctx context.Context, client *dockerclient.Client, id string, files map[string][]byte) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for path, value := range files {
		if err := writer.WriteHeader(&tar.Header{Name: path, Mode: 0o600, Size: int64(len(value))}); err != nil {
			return err
		}
		if _, err := writer.Write(value); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.CopyToContainer(ctx, id, "/", &archive, container.CopyToContainerOptions{})
}

// waitRegistry returns once the registry answers HTTP (401 without credentials).
func waitRegistry(ctx context.Context, url string, alive func(context.Context) error) error {
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := alive(ctx); err != nil {
			return false, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, nil // keep polling until zot listens
		}
		_ = resp.Body.Close()
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("wait for registry on %s: %w", url, err)
	}
	return nil
}

// DeleteRegistry removes the container, then the data.
func (i *Infra) DeleteRegistry(ctx context.Context, name string) error {
	client, err := dockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if err := client.ContainerRemove(ctx, registryPrefix+name, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove container: %w", err)
	}
	return os.RemoveAll(i.registryDir(name))
}

// StopRegistries removes every registry container on the lab network and keeps the data.
func (i *Infra) StopRegistries(ctx context.Context) error {
	client, err := dockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	rows, err := i.registryContainers(ctx, client)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range rows {
		if err := client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove registry %s: %w", c.Labels[registryLabel], err))
		}
	}
	return errors.Join(errs...)
}

// Registries lists the registry containers on the lab network.
func (i *Infra) Registries(ctx context.Context) ([]infra.RegistryInfo, error) {
	client, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	rows, err := i.registryContainers(ctx, client)
	if err != nil {
		return nil, err
	}
	out := make([]infra.RegistryInfo, 0, len(rows))
	for _, c := range rows {
		out = append(out, infra.RegistryInfo{Name: c.Labels[registryLabel], IP: c.Labels[instanceIPLabel], Running: c.State == "running"})
	}
	return out, nil
}

// RegistryImages lists tags through the registry API with the pull credential.
func (i *Infra) RegistryImages(ctx context.Context, spec infra.RegistrySpec) ([]infra.RegistryImage, error) {
	client, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	observed, err := client.ContainerInspect(ctx, registryPrefix+spec.Name)
	if err != nil {
		return nil, fmt.Errorf("inspect registry: %w", err)
	}
	return registryImages(ctx, "http://"+observed.Config.Labels[instanceIPLabel]+":5000", spec, i.registryDir(spec.Name))
}

type registryManifest struct {
	Config    struct{ Size int64 }
	Layers    []struct{ Size int64 }
	Manifests []struct{ Digest string }
}

// registryImages walks catalog, tags and manifests. Pushed is the mtime of the manifest blob in
// zot's OCI layout under dataDir; the distribution API has no push time.
// ponytail: one catalog page; add ?n=&last= paging if a registry outgrows zot's default page.
func registryImages(ctx context.Context, base string, spec infra.RegistrySpec, dataDir string) ([]infra.RegistryImage, error) {
	var catalog struct{ Repositories []string }
	if _, err := registryGet(ctx, base, "/v2/_catalog", spec, &catalog); err != nil {
		return nil, err
	}
	var out []infra.RegistryImage
	for _, repo := range catalog.Repositories {
		var tags struct{ Tags []string }
		if _, err := registryGet(ctx, base, "/v2/"+repo+"/tags/list", spec, &tags); err != nil {
			return nil, err
		}
		for _, tag := range tags.Tags {
			size, digest, err := manifestBytes(ctx, base, repo, tag, spec)
			if err != nil {
				return nil, err
			}
			image := infra.RegistryImage{Repository: repo, Tag: tag, Bytes: size}
			if algo, hex, ok := strings.Cut(digest, ":"); ok {
				if info, err := os.Stat(filepath.Join(dataDir, repo, "blobs", algo, hex)); err == nil {
					image.Pushed = info.ModTime()
				}
			}
			out = append(out, image)
		}
	}
	slices.SortFunc(out, func(a, b infra.RegistryImage) int {
		return strings.Compare(a.Repository+"\x00"+a.Tag, b.Repository+"\x00"+b.Tag)
	})
	return out, nil
}

// manifestBytes adds config and layer sizes, descending into image indexes.
func manifestBytes(ctx context.Context, base, repo, ref string, spec infra.RegistrySpec) (int64, string, error) {
	var m registryManifest
	digest, err := registryGet(ctx, base, "/v2/"+repo+"/manifests/"+ref, spec, &m)
	if err != nil {
		return 0, "", err
	}
	size := m.Config.Size
	for _, layer := range m.Layers {
		size += layer.Size
	}
	for _, child := range m.Manifests {
		n, _, err := manifestBytes(ctx, base, repo, child.Digest, spec)
		if err != nil {
			return 0, "", err
		}
		size += n
	}
	return size, digest, nil
}

func registryGet(ctx context.Context, base, path string, spec infra.RegistrySpec, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(spec.PullUser, spec.PullPassword)
	req.Header.Set("Accept", registryManifestAccept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry %s: %s", path, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return "", fmt.Errorf("registry %s: %w", path, err)
	}
	return resp.Header.Get("Docker-Content-Digest"), nil
}

// RegistryBytes sums file sizes under the registry's data directory; no directory is 0.
func (i *Infra) RegistryBytes(_ context.Context, name string) (int64, error) {
	var total int64
	err := filepath.WalkDir(i.registryDir(name), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			var info fs.FileInfo
			if info, err = d.Info(); err == nil {
				total += info.Size()
			}
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil // a missing directory, or a blob zot just collected
		}
		return err
	})
	return total, err
}
