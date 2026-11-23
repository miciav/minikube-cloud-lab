package minikube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	dockerclient "github.com/docker/docker/client"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

const (
	imageLabel        = "minikube-cloud-lab.image"
	imageHelperPrefix = "mcl-imgbuild-"
	// copyVar runs in a helper with the source /var read-only at /mnt/from-var and the
	// target volume at /var; cp -a keeps owners, modes, links, xattrs and device nodes.
	copyVar = "cp -a /mnt/from-var/. /var/"
	// sanitizeImage strips per-instance identity from a captured system and puts the seed's
	// ds-identify policy back to the base image's (seedCloudInit enables it again for user-data).
	// ROOT prefixes every path for tests and is empty in production.
	sanitizeImage = `set -eu
R=${ROOT:-}
disks="$R/var/lib/mcl-disk-images"
[ ! -d "$disks" ] || find "$disks" -mindepth 1 -delete
rm -rf "$R"/etc/ssh/ssh_host_* "$R"/etc/cloud/mcl-seed/* "$R/etc/cloud/cloud.cfg.d/99-mcl.cfg"
if [ "$(cat "$R/etc/cloud/ds-identify.cfg" 2>/dev/null)" = "policy: enabled" ]; then printf 'policy: disabled\n' > "$R/etc/cloud/ds-identify.cfg"; fi
keys="$R/home/$SSH_USER/.ssh/authorized_keys"
if [ -f "$keys" ]; then
	grep -vxF -e "$SOURCE_KEY" "$keys" > "$keys.mcl" || [ $? -eq 1 ]
	cat "$keys.mcl" > "$keys" && rm -f "$keys.mcl"
fi
if command -v cloud-init >/dev/null; then cloud-init clean --logs; fi
: > "$R/etc/machine-id"
unit="$R/etc/systemd/system/ssh.service.d"
if [ ! -e "$unit/10-host-keys.conf" ]; then
	mkdir -p "$unit"
	printf '[Service]\nExecStartPre=\nExecStartPre=/usr/bin/ssh-keygen -A\nExecStartPre=/usr/sbin/sshd -t\n' > "$unit/10-host-keys.conf"
fi`
)

var _ infra.Images = (*Infra)(nil)

func imageRef(name string) string    { return "localhost/minikube-cloud-lab/image-" + name + ":latest" }
func imageVolume(name string) string { return "mcl-image-" + name + "-var" }
func imageHelper(name string) string { return imageHelperPrefix + name }

// helperContainer idles offline with an instance's privileges, so the copy can recreate
// everything the instance wrote. The target skips copy-up: the image's own /var would
// otherwise resurrect files the student deleted. entrypoint says the image has one to replace;
// otherwise the helper idles through Cmd, because Docker cannot commit an empty entrypoint back.
func helperContainer(image, network, from, to, name string, entrypoint, podman bool) (*container.Config, *container.HostConfig) {
	cfg := &container.Config{
		Image: image, Cmd: []string{"sleep", "infinity"}, StopSignal: "SIGKILL",
		Labels: map[string]string{imageLabel: name, instanceNetLabel: network},
	}
	if entrypoint {
		cfg.Entrypoint, cfg.Cmd = cfg.Cmd, nil
	}
	// Podman's compat API ignores Mounts' NoCopy but honours the bind option.
	target := to + ":/var:nocopy"
	if podman {
		target += ",exec"
	}
	host := &container.HostConfig{
		Privileged: true, SecurityOpt: instanceSecurity(podman), NetworkMode: "none",
		Binds: []string{from + ":/mnt/from-var:ro", target},
	}
	return cfg, host
}

func cleanupFilters(network string) (helpers, images filters.Args) {
	owned := []filters.KeyValuePair{filters.Arg("label", imageLabel), filters.Arg("label", instanceNetLabel+"="+network)}
	helpers = filters.NewArgs(append(owned, filters.Arg("name", "^/?"+imageHelperPrefix))...)
	images = filters.NewArgs(append(owned, filters.Arg("dangling", "true"))...)
	return helpers, images
}

// isImageHelper double-checks the runtime filter: clones carry the same labels.
func isImageHelper(c container.Summary) bool {
	for _, name := range c.Names {
		if strings.HasPrefix(strings.TrimPrefix(name, "/"), imageHelperPrefix) {
			return true
		}
	}
	return false
}

// isUntaggedImage double-checks the dangling filter, so a personal image is never removed.
func isUntaggedImage(image imagetypes.Summary) bool {
	for _, tag := range image.RepoTags {
		if tag != "<none>:<none>" {
			return false
		}
	}
	return true
}

// cleanupImageHelpers removes what an interrupted capture or launch left behind.
func (i *Infra) cleanupImageHelpers(ctx context.Context, client *dockerclient.Client) error {
	helpers, images := cleanupFilters(i.network)
	rows, err := client.ContainerList(ctx, container.ListOptions{All: true, Filters: helpers})
	if err != nil {
		return fmt.Errorf("list image helpers: %w", err)
	}
	var failures []error
	for _, c := range rows {
		if !isImageHelper(c) {
			continue
		}
		if err := client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			failures = append(failures, err)
		}
	}
	dangling, err := client.ImageList(ctx, imagetypes.ListOptions{Filters: images})
	if err != nil {
		return errors.Join(append(failures, fmt.Errorf("list dangling images: %w", err))...)
	}
	for _, image := range dangling {
		if !isUntaggedImage(image) {
			continue
		}
		// A conflict is a parent of a personal image; it goes when that image is deleted.
		if _, err := client.ImageRemove(ctx, image.ID, imagetypes.RemoveOptions{PruneChildren: true}); err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (i *Infra) cleanupImageHelpersLogged(ctx context.Context, client *dockerclient.Client) {
	if err := i.cleanupImageHelpers(ctx, client); err != nil {
		slog.Warn("image helper cleanup failed", "error", err)
	}
}

// withHelper copies volume from into volume to inside a helper from image, runs then (if any)
// on the helper and removes it.
func (i *Infra) withHelper(ctx context.Context, client *dockerclient.Client, image, from, to, name string, entrypoint bool, then func(id string) error) error {
	cfg, host := helperContainer(image, i.network, from, to, name, entrypoint, instanceRootless())
	created, err := client.ContainerCreate(ctx, cfg, host, nil, nil, imageHelper(name))
	if err != nil {
		return fmt.Errorf("create helper: %w", err)
	}
	defer func() {
		if err := client.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true}); err != nil {
			slog.Warn("remove image helper", "helper", imageHelper(name), "error", err)
		}
	}()
	if err := client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start helper: %w", err)
	}
	if err := execRootWithin(ctx, client, created.ID, nil, []string{"sh", "-c", copyVar}, 30*time.Minute); err != nil {
		return fmt.Errorf("copy /var: %w", err)
	}
	if then == nil {
		return nil
	}
	return then(created.ID)
}

// seedVar fills a new instance volume with the personal image's /var before first boot.
func (i *Infra) seedVar(ctx context.Context, client *dockerclient.Client, name, target string) error {
	i.cleanupImageHelpersLogged(ctx, client)
	return i.withHelper(ctx, client, imageRef(name), imageVolume(name), target, name, true, nil)
}

// restoreChanges gives the final image the source's start configuration back. Empty fields
// are already empty on the helper, and Env needs no change because the helper adds none.
func restoreChanges(source *container.Config, name, network string) []string {
	changes := ownedLabels(name, network)
	if len(source.Entrypoint) > 0 {
		entrypoint, _ := json.Marshal(source.Entrypoint)
		changes = append(changes, "ENTRYPOINT "+string(entrypoint))
	}
	if len(source.Cmd) > 0 {
		cmd, _ := json.Marshal(source.Cmd)
		changes = append(changes, "CMD "+string(cmd))
	}
	if source.StopSignal != "" {
		changes = append(changes, "STOPSIGNAL "+source.StopSignal)
	}
	return changes
}

func ownedLabels(name, network string) []string {
	return []string{"LABEL " + imageLabel + "=" + name + " " + instanceNetLabel + "=" + network}
}

// imageMissing fails when the runtime already holds a resource the capture would overwrite.
func imageMissing(ctx context.Context, client *dockerclient.Client, name string) error {
	if _, err := client.ImageInspect(ctx, imageRef(name)); err == nil {
		return fmt.Errorf("image %s already exists in the container runtime", imageRef(name))
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image: %w", err)
	}
	if _, err := client.VolumeInspect(ctx, imageVolume(name)); err == nil {
		return fmt.Errorf("volume %s already exists", imageVolume(name))
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect volume: %w", err)
	}
	return nil
}

// CaptureImage commits a stopped instance's system and copies its /var into an image volume.
func (i *Infra) CaptureImage(ctx context.Context, spec infra.ImageSpec) (captured infra.CapturedImage, err error) {
	if i.network == "" {
		return captured, errors.New("personal images require CLOUD_DOCKER_NETWORK")
	}
	client, err := dockerClient()
	if err != nil {
		return captured, err
	}
	defer func() { _ = client.Close() }()
	i.cleanupImageHelpersLogged(ctx, client)
	source, err := client.ContainerInspect(ctx, instancePrefix+spec.Instance)
	if err != nil {
		return captured, fmt.Errorf("inspect instance: %w", err)
	}
	if source.Config == nil || source.State == nil || source.State.Running {
		return captured, fmt.Errorf("instance %s must be stopped", spec.Instance)
	}
	if err := imageMissing(ctx, client, spec.Name); err != nil {
		return captured, err
	}
	cleanup := context.WithoutCancel(ctx)
	defer func() {
		if err != nil {
			_ = i.DeleteImage(cleanup, spec.Name)
		}
	}()
	// The commit holds the system without /var, which is a volume.
	rootfs, err := client.ContainerCommit(ctx, source.ID, container.CommitOptions{Changes: ownedLabels(spec.Name, i.network)})
	if err != nil {
		return captured, fmt.Errorf("commit instance: %w", err)
	}
	defer func() {
		// A conflict means the final image builds on it; it goes with that image.
		if _, err := client.ImageRemove(cleanup, rootfs.ID, imagetypes.RemoveOptions{PruneChildren: true}); err != nil && !cerrdefs.IsConflict(err) && !cerrdefs.IsNotFound(err) {
			slog.Warn("remove intermediate image", "image", spec.Name, "error", err)
		}
	}()
	if _, err := client.VolumeCreate(ctx, volume.CreateOptions{Name: imageVolume(spec.Name), Labels: map[string]string{imageLabel: spec.Name, instanceNetLabel: i.network}}); err != nil {
		return captured, fmt.Errorf("create image volume: %w", err)
	}
	err = i.withHelper(ctx, client, rootfs.ID, instancePrefix+spec.Instance, imageVolume(spec.Name), spec.Name, len(source.Config.Entrypoint) > 0, func(helper string) error {
		captured.CloudInit, err = i.finishImage(ctx, client, helper, spec, source.Config)
		return err
	})
	if err != nil {
		return captured, err
	}
	captured.Bytes, err = imageBytes(ctx, client, spec.Name)
	return captured, err
}

// finishImage sanitizes the helper's system and /var and commits it as the personal image,
// reporting whether cloud-init is installed.
func (i *Infra) finishImage(ctx context.Context, client *dockerclient.Client, helper string, spec infra.ImageSpec, source *container.Config) (cloudInit bool, err error) {
	env := []string{"SSH_USER=" + spec.SSHUser, "SOURCE_KEY=" + spec.SourceKey}
	// cloud-init clean starts Python, which can outlast execRoot's ten seconds.
	if err := execRootWithin(ctx, client, helper, env, []string{"sh", "-c", sanitizeImage}, time.Minute); err != nil {
		return false, fmt.Errorf("sanitize image: %w", err)
	}
	var exit guestExitCode
	switch err := execRoot(ctx, client, helper, nil, "command -v cloud-init"); {
	case err == nil:
		cloudInit = true
	case !errors.As(err, &exit):
		return false, fmt.Errorf("detect cloud-init: %w", err)
	}
	if _, err := client.ContainerCommit(ctx, helper, container.CommitOptions{Reference: imageRef(spec.Name), Changes: restoreChanges(source, spec.Name, i.network)}); err != nil {
		return false, fmt.Errorf("commit image: %w", err)
	}
	return cloudInit, nil
}

// imageBytes is the image size, plus the volume's when the runtime reports it (rarely).
func imageBytes(ctx context.Context, client *dockerclient.Client, name string) (int64, error) {
	observed, err := client.ImageInspect(ctx, imageRef(name))
	if err != nil {
		return 0, fmt.Errorf("inspect image: %w", err)
	}
	size := observed.Size
	if disk, err := client.VolumeInspect(ctx, imageVolume(name)); err == nil && disk.UsageData != nil && disk.UsageData.Size > 0 {
		size += disk.UsageData.Size
	}
	return size, nil
}

// DeleteImage removes a personal image and its /var volume; absent parts are success.
func (i *Infra) DeleteImage(ctx context.Context, name string) error {
	client, err := dockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if _, err := client.ImageRemove(ctx, imageRef(name), imagetypes.RemoveOptions{PruneChildren: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove image: %w", err)
	}
	if err := client.VolumeRemove(ctx, imageVolume(name), false); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove image volume: %w", err)
	}
	return nil
}

// ImageExists reports whether both the image and its /var volume are present.
func (i *Infra) ImageExists(ctx context.Context, name string) (bool, error) {
	client, err := dockerClient()
	if err != nil {
		return false, err
	}
	defer func() { _ = client.Close() }()
	if _, err := client.ImageInspect(ctx, imageRef(name)); cerrdefs.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := client.VolumeInspect(ctx, imageVolume(name)); cerrdefs.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}
