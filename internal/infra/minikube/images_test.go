package minikube

import (
	"context"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

func TestImageNames(t *testing.T) {
	if got := imageRef("web"); got != "localhost/minikube-cloud-lab/image-web:latest" {
		t.Fatal(got)
	}
	if got := imageVolume("web"); got != "mcl-image-web-var" {
		t.Fatal(got)
	}
	if got := imageHelper("web"); got != "mcl-imgbuild-web" {
		t.Fatal(got)
	}
}

func TestHelperContainerIsPrivilegedOfflineAndNotAnInstance(t *testing.T) {
	for _, podman := range []bool{false, true} {
		cfg, host := helperContainer("base@sha", "lab", "mcl-vm-src", "mcl-image-web-var", "web", true, podman)
		if cfg.Image != "base@sha" || !reflect.DeepEqual([]string(cfg.Entrypoint), []string{"sleep", "infinity"}) || cfg.Cmd != nil || cfg.StopSignal != "SIGKILL" {
			t.Fatalf("config: %+v", cfg)
		}
		// Docker cannot commit an empty entrypoint back, so images without one idle through Cmd.
		if plain, _ := helperContainer("base@sha", "lab", "a", "b", "web", false, podman); plain.Entrypoint != nil || !reflect.DeepEqual([]string(plain.Cmd), []string{"sleep", "infinity"}) {
			t.Fatalf("no entrypoint: %+v", plain)
		}
		if _, ok := cfg.Labels[instanceLabel]; ok || cfg.Labels[imageLabel] != "web" || cfg.Labels[instanceNetLabel] != "lab" {
			t.Fatalf("labels: %v", cfg.Labels)
		}
		_, instance, _ := instanceContainer(infra.InstanceSpec{Name: "x"}, "i", "lab", netip.MustParseAddr("172.30.0.200"), podman)
		if !host.Privileged || !reflect.DeepEqual(host.SecurityOpt, instance.SecurityOpt) || host.NetworkMode != "none" || host.RestartPolicy.Name != "" {
			t.Fatalf("host: %+v", host)
		}
		// The target skips copy-up, so files deleted from the source /var stay deleted. Podman's
		// compat API ignores Mounts' NoCopy but honours the bind option.
		target := "mcl-image-web-var:/var:nocopy"
		if podman {
			target += ",exec"
		}
		if !reflect.DeepEqual(host.Binds, []string{"mcl-vm-src:/mnt/from-var:ro", target}) || host.Mounts != nil {
			t.Fatalf("binds: %v %v", host.Binds, host.Mounts)
		}
	}
}

func TestInstanceImageRefCustomNeverPulls(t *testing.T) {
	ref, kicbase, err := instanceImageRef(infra.InstanceSpec{Image: "web", Custom: true})
	if err != nil || kicbase || ref != imageRef("web") {
		t.Fatalf("custom: %q %v %v", ref, kicbase, err)
	}
	pulls := pullingDocker(t, `{"status":"done"}`)
	client, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instanceImage(context.Background(), client, infra.InstanceSpec{Image: "web", Custom: true}); err == nil || err.Error() != "image web is missing from the container runtime; recreate it" {
		t.Fatalf("missing custom image: %v", err)
	}
	if *pulls != 0 {
		t.Fatalf("pulled a personal image %d times", *pulls)
	}
}

func TestSanitizeScriptRemovesOnlyTheSourceKey(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := "ssh-ed25519 AAAAsource"
	write("home/ubuntu/.ssh/authorized_keys", "\nssh-ed25519 AAAAstudent me@laptop\n"+source+"\nssh-ed25519 AAAAsource-prefix\n")
	write("var/lib/mcl-disk-images/disk-1.img", "data")
	write("etc/ssh/ssh_host_ed25519_key", "secret")
	write("etc/ssh/sshd_config", "keep")
	write("etc/cloud/mcl-seed/user-data", "#cloud-config")
	write("etc/cloud/cloud.cfg.d/99-mcl.cfg", "seed")
	write("etc/cloud/cloud.cfg.d/90-lab-user.cfg", "keep")
	write("etc/cloud/ds-identify.cfg", "policy: enabled\n")
	write("etc/machine-id", "0123456789abcdef\n")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "cloud-init"), []byte("#!/bin/sh\necho \"$@\" > \"$ROOT/cloud-init-called\"\n"), 0o700); err != nil { // #nosec G306 -- test stub must be executable.
		t.Fatal(err)
	}
	sanitize := func() {
		t.Helper()
		run := exec.CommandContext(t.Context(), "sh", "-c", sanitizeImage) // #nosec G204 -- fixed script under test.
		run.Env = append(os.Environ(), "ROOT="+root, "SSH_USER=ubuntu", "SOURCE_KEY="+source, "PATH="+bin+":"+os.Getenv("PATH"))
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("sanitize: %v\n%s", err, out)
		}
	}
	sanitize()
	read := func(path string) string {
		data, err := os.ReadFile(filepath.Join(root, path)) // #nosec G304 -- paths under the test root.
		if err != nil {
			return "<absent>"
		}
		return string(data)
	}
	if got := read("home/ubuntu/.ssh/authorized_keys"); got != "\nssh-ed25519 AAAAstudent me@laptop\nssh-ed25519 AAAAsource-prefix\n" {
		t.Fatalf("authorized_keys: %q", got)
	}
	for _, gone := range []string{"var/lib/mcl-disk-images/disk-1.img", "etc/ssh/ssh_host_ed25519_key", "etc/cloud/mcl-seed/user-data", "etc/cloud/cloud.cfg.d/99-mcl.cfg"} {
		if read(gone) != "<absent>" {
			t.Errorf("%s survived", gone)
		}
	}
	for path, want := range map[string]string{
		"etc/ssh/sshd_config":                   "keep",
		"etc/cloud/cloud.cfg.d/90-lab-user.cfg": "keep",
		"etc/machine-id":                        "",
		// The seed's policy goes back to the base image's, so clones without user-data skip cloud-init.
		"etc/cloud/ds-identify.cfg":                          "policy: disabled\n",
		"cloud-init-called":                                  "clean --logs\n",
		"etc/systemd/system/ssh.service.d/10-host-keys.conf": "[Service]\nExecStartPre=\nExecStartPre=/usr/bin/ssh-keygen -A\nExecStartPre=/usr/sbin/sshd -t\n",
	} {
		if got := read(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/mcl-disk-images")); err != nil {
		t.Errorf("disk mount point removed: %v", err)
	}

	// A ds-identify policy and a drop-in the seed did not write stay untouched.
	write("etc/cloud/ds-identify.cfg", "policy: search,found=all\n")
	write("etc/systemd/system/ssh.service.d/10-host-keys.conf", "custom")
	sanitize()
	if read("etc/cloud/ds-identify.cfg") != "policy: search,found=all\n" || read("etc/systemd/system/ssh.service.d/10-host-keys.conf") != "custom" {
		t.Fatal("sanitize overwrote files the seed did not write")
	}
	// An image without the file does not get one.
	if err := os.Remove(filepath.Join(root, "etc/cloud/ds-identify.cfg")); err != nil {
		t.Fatal(err)
	}
	sanitize()
	if read("etc/cloud/ds-identify.cfg") != "<absent>" {
		t.Fatal("sanitize created ds-identify.cfg")
	}
}

func TestRestoreChangesSkipEmptyStartFields(t *testing.T) {
	got := restoreChanges(&container.Config{Cmd: []string{"/sbin/init"}, StopSignal: "SIGRTMIN+3"}, "web", "lab")
	want := []string{"LABEL minikube-cloud-lab.image=web minikube-cloud-lab.network=lab", `CMD ["/sbin/init"]`, "STOPSIGNAL SIGRTMIN+3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ubuntu: %q", got)
	}
	got = restoreChanges(&container.Config{Entrypoint: []string{"/usr/local/bin/entrypoint", "/sbin/init"}}, "web", "lab")
	if !reflect.DeepEqual(got[1:], []string{`ENTRYPOINT ["/usr/local/bin/entrypoint","/sbin/init"]`}) {
		t.Fatalf("kicbase: %q", got)
	}
}

func TestCleanupHelpersSelectsOwnedOnly(t *testing.T) {
	helpers, images := cleanupFilters("lab")
	if got := helpers.Get("name"); !reflect.DeepEqual(got, []string{"^/?mcl-imgbuild-"}) {
		t.Fatalf("helper names: %v", got)
	}
	if !helpers.ExactMatch("label", imageLabel) || !helpers.ExactMatch("label", instanceNetLabel+"=lab") || len(helpers.Get("label")) != 2 {
		t.Fatalf("helper labels: %v", helpers.Get("label"))
	}
	if !images.ExactMatch("dangling", "true") || !images.ExactMatch("label", imageLabel) || !images.ExactMatch("label", instanceNetLabel+"=lab") {
		t.Fatalf("image filters: %v", images)
	}
}

func TestInstancesSkipImageHelpers(t *testing.T) {
	fakeDocker(t, map[string]string{"/containers/json": `[
		{"Names": ["/mcl-vm-src"], "State": "exited", "Labels": {"minikube-cloud-lab.instance": "src"}},
		{"Names": ["/mcl-imgbuild-web"], "State": "running", "Labels": {"minikube-cloud-lab.instance": "src", "minikube-cloud-lab.image": "web"}}
	]`})
	got, err := (&Infra{network: "lab"}).Instances(context.Background())
	if err != nil || !reflect.DeepEqual(got, []infra.Instance{{Name: "src"}}) {
		t.Fatalf("instances: %+v %v", got, err)
	}
}

func TestCleanupSkipsClonesAndTaggedImages(t *testing.T) {
	owned := map[string]string{imageLabel: "web", instanceNetLabel: "lab"}
	if isImageHelper(container.Summary{Names: []string{"/mcl-vm-x"}, Labels: owned}) {
		t.Fatal("clone selected as helper")
	}
	if !isImageHelper(container.Summary{Names: []string{"/mcl-imgbuild-web"}, Labels: owned}) || !isImageHelper(container.Summary{Names: []string{"mcl-imgbuild-web"}}) {
		t.Fatal("helper not selected")
	}
	if isUntaggedImage(imagetypes.Summary{RepoTags: []string{"localhost/minikube-cloud-lab/image-web:latest"}, Labels: owned}) {
		t.Fatal("tagged personal image selected")
	}
	if !isUntaggedImage(imagetypes.Summary{Labels: owned}) || !isUntaggedImage(imagetypes.Summary{RepoTags: []string{"<none>:<none>"}}) {
		t.Fatal("dangling image not selected")
	}
}
