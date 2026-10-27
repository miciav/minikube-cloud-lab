package minikube

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"k8s.io/minikube/pkg/minikube/assets"
	"k8s.io/minikube/pkg/minikube/cruntime"
)

type mirrorCapture struct {
	cruntime.CommandRunner
	data    []byte
	path    string
	failure error
}

func (c *mirrorCapture) Copy(f assets.CopyableFile) error {
	if c.failure != nil {
		return c.failure
	}
	c.path = f.GetTargetPath()
	b, err := io.ReadAll(f)
	c.data = b
	return err
}

func TestDockerMirrorPreservesGeneratedConfiguration(t *testing.T) {
	for _, existing := range []string{"", `,"registry-mirrors":["https://other.example","https://mirror.gcr.io"]`} {
		c := &mirrorCapture{}
		r := &dockerMirrorRunner{CommandRunner: c}
		input := `{"exec-opts":["native.cgroupdriver=systemd"],"log-driver":"json-file","log-opts":{"max-size":"100m"}` + existing + `}`
		if err := r.Copy(assets.NewMemoryAsset([]byte(input), "/etc/docker", "daemon.json", "0644")); err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(c.data, &got); err != nil {
			t.Fatal(err)
		}
		var mirrors []string
		if err := json.Unmarshal(got["registry-mirrors"], &mirrors); err != nil {
			t.Fatal(err)
		}
		want := []string{"https://mirror.gcr.io"}
		if existing != "" {
			want = []string{"https://other.example", "https://mirror.gcr.io"}
		}
		if !r.applied || c.path != "/etc/docker/daemon.json" || !reflect.DeepEqual(mirrors, want) || string(got["exec-opts"]) != `["native.cgroupdriver=systemd"]` || string(got["log-driver"]) != `"json-file"` {
			t.Fatalf("mirror or existing config lost: %s", c.data)
		}
	}
}

func TestDockerMirrorLeavesOtherAssetsUntouched(t *testing.T) {
	c := &mirrorCapture{}
	r := &dockerMirrorRunner{CommandRunner: c}
	if err := r.Copy(assets.NewMemoryAsset([]byte("other"), "/tmp", "config", "0644")); err != nil {
		t.Fatal(err)
	}
	if r.applied || c.path != "/tmp/config" || string(c.data) != "other" {
		t.Fatalf("unrelated file changed: %+v", c)
	}
}

func TestDockerMirrorRejectsInvalidOrFailedCopy(t *testing.T) {
	for _, input := range []string{`not json`, `null`, `{"registry-mirrors":"wrong"}`} {
		c := &mirrorCapture{}
		r := &dockerMirrorRunner{CommandRunner: c}
		if err := r.Copy(assets.NewMemoryAsset([]byte(input), "/etc/docker", "daemon.json", "0644")); err == nil || r.applied || c.data != nil {
			t.Fatalf("accepted invalid config %s", input)
		}
	}
	c := &mirrorCapture{failure: errors.New("copy failed")}
	r := &dockerMirrorRunner{CommandRunner: c}
	if err := r.Copy(assets.NewMemoryAsset([]byte(`{}`), "/etc/docker", "daemon.json", "0644")); err == nil || r.applied {
		t.Fatalf("hidden write failure: %v", err)
	}
}
