//go:build integration

package minikube

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	"golang.org/x/crypto/ssh"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

func authorizedKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// TestGoldenImageRoundTrip captures an Ubuntu instance and launches a clone from it. It runs in a
// container on MCL_IMAGE_IT_NETWORK (an aligned private /23 bridge) with the runtime socket mounted,
// so SSH to instance addresses works; set CLOUD_ROOTLESS=true on Podman.
func TestGoldenImageRoundTrip(t *testing.T) {
	network := os.Getenv("MCL_IMAGE_IT_NETWORK")
	if network == "" {
		t.Skip("MCL_IMAGE_IT_NETWORK is not set")
	}
	ctx := context.Background()
	i := &Infra{network: network}
	client, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"gclone", "gsrc"} {
			if err := i.DeleteInstance(ctx, name); err != nil {
				t.Error(err)
			}
		}
		if err := i.DeleteImage(ctx, "gimg"); err != nil {
			t.Error(err)
		}
		_ = client.Close()
	})
	sourceKey, cloneKey, studentKey := authorizedKey(t), authorizedKey(t), authorizedKey(t)
	shape := infra.MachineShape{CPUs: 1, MemoryMiB: 1024}
	src, err := i.CreateInstance(ctx, infra.InstanceSpec{Name: "gsrc", Image: "ubuntu-26.04", Shape: shape, SSHUser: "ubuntu", AuthorizedKey: sourceKey})
	if err != nil {
		t.Fatal(err)
	}
	if err := execRoot(ctx, client, "mcl-vm-gsrc", []string{"KEY=" + studentKey}, `echo golden > /etc/golden-marker && echo golden > /var/lib/golden-marker &&
rm /var/log/bootstrap.log && echo "$KEY" >> /home/ubuntu/.ssh/authorized_keys`); err != nil {
		t.Fatal(err)
	}
	if err := i.StopInstance(ctx, "gsrc"); err != nil {
		t.Fatal(err)
	}
	captured, err := i.CaptureImage(ctx, infra.ImageSpec{Name: "gimg", Instance: "gsrc", SSHUser: "ubuntu", SourceKey: sourceKey})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("captured %+v", captured)
	if !captured.CloudInit || captured.Bytes <= 0 {
		t.Fatalf("captured: %+v", captured)
	}
	if exists, err := i.ImageExists(ctx, "gimg"); err != nil || !exists {
		t.Fatalf("exists: %v %v", exists, err)
	}
	observed, err := client.ImageInspect(ctx, imageRef("gimg"))
	if err != nil {
		t.Fatal(err)
	}
	// Podman reports the signal by number.
	if c := observed.Config; len(c.Entrypoint) != 0 || strings.Join(c.Cmd, " ") != "/sbin/init" || (c.StopSignal != "SIGRTMIN+3" && c.StopSignal != "37") || c.Labels[imageLabel] != "gimg" {
		t.Fatalf("image config: entrypoint %q cmd %q stop %q labels %v", c.Entrypoint, c.Cmd, c.StopSignal, c.Labels)
	}
	assertNoLeftovers(t, i, client)

	clone, err := i.CreateInstance(ctx, infra.InstanceSpec{Name: "gclone", Image: "gimg", Custom: true, Shape: shape, SSHUser: "ubuntu", AuthorizedKey: cloneKey})
	if err != nil {
		t.Fatal(err)
	}
	if clone.HostKey == src.HostKey {
		t.Fatal("clone reuses the source host key")
	}
	assertNoLeftovers(t, i, client)
	for check, script := range map[string]string{
		"system file": "test -f /etc/golden-marker",
		"/var file":   "test -f /var/lib/golden-marker",
		"deleted /var file stays deleted (no copy-up)": "test ! -e /var/log/bootstrap.log",
		"student key kept":  `grep -qxF "$STUDENT" /home/ubuntu/.ssh/authorized_keys`,
		"clone key added":   `grep -qxF "$CLONE" /home/ubuntu/.ssh/authorized_keys`,
		"source key gone":   `! grep -qF "$SOURCE" /home/ubuntu/.ssh/authorized_keys`,
		"fresh machine-id":  `test -s /etc/machine-id`,
		"seed policy reset": `test "$(cat /etc/cloud/ds-identify.cfg)" = "policy: disabled"`,
	} {
		if err := execRoot(ctx, client, "mcl-vm-gclone", []string{"STUDENT=" + studentKey, "CLONE=" + cloneKey, "SOURCE=" + sourceKey}, script); err != nil {
			t.Errorf("%s: %v", check, err)
		}
	}
	instances, err := i.Instances(ctx)
	if err != nil || len(instances) != 2 {
		t.Fatalf("instances: %+v %v", instances, err)
	}
	if err := i.DeleteInstance(ctx, "gclone"); err != nil {
		t.Fatal(err)
	}
	if err := i.DeleteImage(ctx, "gimg"); err != nil {
		t.Fatal(err)
	}
	if exists, err := i.ImageExists(ctx, "gimg"); err != nil || exists {
		t.Fatalf("after delete: %v %v", exists, err)
	}
}

func assertNoLeftovers(t *testing.T, i *Infra, client interface {
	ContainerList(context.Context, container.ListOptions) ([]container.Summary, error)
	ImageList(context.Context, imagetypes.ListOptions) ([]imagetypes.Summary, error)
}) {
	t.Helper()
	helpers, images := cleanupFilters(i.network)
	rows, err := client.ContainerList(context.Background(), container.ListOptions{All: true, Filters: helpers})
	if err != nil || len(rows) != 0 {
		t.Errorf("helpers left: %+v %v", rows, err)
	}
	dangling, err := client.ImageList(context.Background(), imagetypes.ListOptions{Filters: images})
	if err != nil || len(dangling) != 0 {
		t.Errorf("dangling images left: %+v %v", dangling, err)
	}
}

// TestGoldenImageInnerDocker captures a debian-12 (kicbase) source whose inner Docker holds an image
// and a container, then checks that a clone runs that image without pulling. Same setup as above,
// in an image with the Docker CLI (such as the cloud image), which minikube uses to find kicbase.
func TestGoldenImageInnerDocker(t *testing.T) {
	network := os.Getenv("MCL_IMAGE_IT_NETWORK")
	if network == "" {
		t.Skip("MCL_IMAGE_IT_NETWORK is not set")
	}
	ctx := context.Background()
	i := &Infra{network: network}
	client, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"gdclone", "gdsrc"} {
			if err := i.DeleteInstance(ctx, name); err != nil {
				t.Error(err)
			}
		}
		if err := i.DeleteImage(ctx, "gdimg"); err != nil {
			t.Error(err)
		}
		_ = client.Close()
	})
	sourceKey := authorizedKey(t)
	shape := infra.MachineShape{CPUs: 1, MemoryMiB: 1024}
	if _, err := i.CreateInstance(ctx, infra.InstanceSpec{Name: "gdsrc", Image: "debian-12", Shape: shape, SSHUser: "docker", AuthorizedKey: sourceKey}); err != nil {
		t.Fatal(err)
	}
	// The pull needs the network; the ID goes to the rootfs so the clone can compare it.
	if err := execRootWithin(ctx, client, "mcl-vm-gdsrc", nil, []string{"sh", "-c", `until docker info >/dev/null 2>&1; do sleep 1; done
docker pull -q nginx:1.29-alpine && docker create --name web nginx:1.29-alpine &&
docker image inspect -f '{{.Id}}' nginx:1.29-alpine > /etc/golden-nginx-id`}, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := i.StopInstance(ctx, "gdsrc"); err != nil {
		t.Fatal(err)
	}
	captured, err := i.CaptureImage(ctx, infra.ImageSpec{Name: "gdimg", Instance: "gdsrc", SSHUser: "docker", SourceKey: sourceKey})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("captured %+v", captured)
	assertNoLeftovers(t, i, client)
	if _, err := i.CreateInstance(ctx, infra.InstanceSpec{Name: "gdclone", Image: "gdimg", Custom: true, Shape: shape, SSHUser: "docker", AuthorizedKey: authorizedKey(t)}); err != nil {
		t.Fatal(err)
	}
	if err := execRootWithin(ctx, client, "mcl-vm-gdclone", nil, []string{"sh", "-c", `until docker info >/dev/null 2>&1; do sleep 1; done
test "$(docker image inspect -f '{{.Id}}' nginx:1.29-alpine)" = "$(cat /etc/golden-nginx-id)" &&
docker container inspect web >/dev/null && docker run --pull=never --rm nginx:1.29-alpine nginx -v`}, 2*time.Minute); err != nil {
		t.Fatalf("inner Docker data in clone: %v", err)
	}
}
