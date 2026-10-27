package minikube

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"

	"k8s.io/minikube/pkg/minikube/assets"
	"k8s.io/minikube/pkg/minikube/cruntime"
)

const dockerHubMirror = "https://mirror.gcr.io"

// dockerMirrorRunner modifies minikube's generated daemon config before it starts Docker.
type dockerMirrorRunner struct {
	cruntime.CommandRunner
	applied bool
}

func (r *dockerMirrorRunner) Copy(file assets.CopyableFile) error {
	if path.Join(file.GetTargetDir(), file.GetTargetName()) != "/etc/docker/daemon.json" {
		return r.CommandRunner.Copy(file)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("decode Docker daemon configuration: %w", err)
	}
	if config == nil {
		return fmt.Errorf("docker daemon configuration must be a JSON object")
	}
	var mirrors []string
	if raw, ok := config["registry-mirrors"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("registry-mirrors must be an array")
		}
		if err := json.Unmarshal(raw, &mirrors); err != nil {
			return fmt.Errorf("decode Docker registry mirrors: %w", err)
		}
	}
	if !slices.Contains(mirrors, dockerHubMirror) {
		mirrors = append(mirrors, dockerHubMirror)
	}
	config["registry-mirrors"], err = json.Marshal(mirrors)
	if err != nil {
		return err
	}
	data, err = json.Marshal(config)
	if err != nil {
		return err
	}
	if err := r.CommandRunner.Copy(assets.NewMemoryAsset(data, file.GetTargetDir(), file.GetTargetName(), file.GetPermissions())); err != nil {
		return err
	}
	r.applied = true
	return nil
}
