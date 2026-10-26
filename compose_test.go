//go:build docker

package distribution

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeConfiguration(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "sample.env")
	if err := os.WriteFile(envPath, []byte("UID=1001\nGID=1001\nDOCKER_GID=998\nHOME=/tmp/cloud-lab-home\nCLOUD_IMAGE=example/cloud:test\nLAB_IMAGE=example/lab:test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "compose", "--env-file", envPath, "-f", "compose.yaml", "--profile", "tools", "config", "--format", "json")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name != "UID" && name != "GID" && name != "DOCKER_GID" && name != "HOME" && name != "CLOUD_IMAGE" && name != "LAB_IMAGE" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compose config: %s (%v)", output, err)
	}
	var config struct {
		Services map[string]struct {
			Image       string
			NetworkMode string `json:"network_mode"`
			Networks    map[string]struct {
				IPv4Address string `json:"ipv4_address"`
			}
			Environment map[string]string
			Command     []string
			Ports       []struct {
				HostIP    string `json:"host_ip"`
				Published string
				Target    int
			}
			User     string
			Restart  string
			GroupAdd []string `json:"group_add"`
			Profiles []string
			Volumes  []struct {
				Source, Target string
				ReadOnly       bool `json:"read_only"`
			}
		}
		Networks map[string]struct {
			Name, Driver string
			IPAM         struct {
				Config []struct{ Subnet, Gateway string }
			}
		}
	}
	if err := json.Unmarshal(output, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Services) != 2 {
		t.Fatalf("services: %+v", config.Services)
	}
	cloud := config.Services["cloud"]
	lab := config.Services["lab"]
	if cloud.Image != "example/cloud:test" || lab.Image != "example/lab:test" {
		t.Fatal("image overrides ignored")
	}
	if cloud.NetworkMode != "" || lab.NetworkMode != "service:cloud" || cloud.User != "1001:1001" || lab.User != "1001:1001" {
		t.Fatalf("network/user: %+v", config.Services)
	}
	network := config.Networks["lab"]
	if network.Name != "minikube-cloud-lab" || network.Driver != "bridge" || len(network.IPAM.Config) != 1 || network.IPAM.Config[0].Subnet != "172.30.0.0/24" || network.IPAM.Config[0].Gateway != "" {
		t.Fatalf("bridge IPAM: %+v", network)
	}
	if len(cloud.Networks) != 1 || cloud.Networks["lab"].IPv4Address != "172.30.0.254" || len(lab.Networks) != 0 {
		t.Fatalf("shared network: cloud=%+v lab=%+v", cloud.Networks, lab.Networks)
	}
	if cloud.Environment["CLOUD_DOCKER_NETWORK"] != "minikube-cloud-lab" || cloud.Environment["CLOUD_DOCKER_IP"] != "172.30.0.254" || lab.Environment["KUBECONFIG"] != "/data/minikube/kubeconfig" {
		t.Fatalf("endpoint configuration: cloud=%+v lab=%+v", cloud.Environment, lab.Environment)
	}
	if strings.Join(cloud.Command, " ") != "serve --data-dir /data --api 0.0.0.0:8080" || len(cloud.Ports) != 1 || cloud.Ports[0].HostIP != "127.0.0.1" || cloud.Ports[0].Target != 8080 || cloud.Ports[0].Published != "8080" || len(lab.Ports) != 0 {
		t.Fatalf("localhost publication: command=%v ports=%+v", cloud.Command, cloud.Ports)
	}
	if cloud.Restart != "unless-stopped" || len(cloud.GroupAdd) != 1 || cloud.GroupAdd[0] != "998" {
		t.Fatalf("cloud settings: %+v", cloud)
	}
	if len(lab.Profiles) != 1 || lab.Profiles[0] != "tools" {
		t.Fatalf("tools profile: %+v", lab)
	}
	expected := map[string]string{"/data": "/tmp/cloud-lab-home/.minikube-cloud-lab", "/kube": "/tmp/cloud-lab-home/.kube", "/var/run/docker.sock": "/var/run/docker.sock"}
	if len(cloud.Volumes) != 3 {
		t.Fatalf("cloud volumes: %+v", cloud.Volumes)
	}
	for _, volume := range cloud.Volumes {
		if expected[volume.Target] != volume.Source || volume.ReadOnly {
			t.Fatalf("cloud volume: %+v", volume)
		}
	}
	if len(lab.Volumes) != 1 || lab.Volumes[0].Target != "/data" || lab.Volumes[0].Source != "/tmp/cloud-lab-home/.minikube-cloud-lab" || !lab.Volumes[0].ReadOnly {
		t.Fatalf("lab volumes: %+v", lab.Volumes)
	}
}
