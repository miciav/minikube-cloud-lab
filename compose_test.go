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
	if err := os.WriteFile(envPath, []byte("UID=1001\nGID=1001\nDOCKER_GID=998\nHOME=/tmp/cloud-lab-home\nCLOUD_IMAGE=example/cloud:test\nLAB_IMAGE=example/lab:test\nPORTAL_IMAGE=example/portal:test\nUBUNTU_IMAGE=example/ubuntu:test\nDEBIAN_IMAGE=example/debian:test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "compose", "--env-file", envPath, "-f", "compose.yaml", "--profile", "tools", "--profile", "portal", "config", "--format", "json")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name != "UID" && name != "GID" && name != "DOCKER_GID" && name != "HOME" && name != "CLOUD_IMAGE" && name != "LAB_IMAGE" && name != "UBUNTU_IMAGE" && name != "DEBIAN_IMAGE" {
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
			User        string
			Restart     string
			GroupAdd    []string `json:"group_add"`
			Profiles    []string
			CapAdd      []string `json:"cap_add"`
			SecurityOpt []string `json:"security_opt"`
			Volumes     []struct {
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
	if len(config.Services) != 5 {
		t.Fatalf("services: %+v", config.Services)
	}
	forward := config.Services["kubectl-forward"]
	if forward.Image != "example/lab:test" || forward.NetworkMode != "" || len(forward.Networks) != 1 || forward.User != "1001:1001" || len(forward.Ports) != 0 || len(forward.Volumes) != 1 || !forward.Volumes[0].ReadOnly || forward.Environment["KUBECONFIG"] != "/data/minikube/kubeconfig" {
		t.Fatalf("forwarding isolation: %+v", forward)
	}
	if _, ok := forward.Networks["lab"]; !ok {
		t.Fatal("forwarding container must reach cluster network")
	}
	portal := config.Services["portal"]
	if len(portal.Profiles) != 1 || portal.Profiles[0] != "portal" || portal.Image != "example/portal:test" || len(portal.Networks) != 1 || len(portal.Volumes) != 0 || len(portal.CapAdd) != 0 || len(portal.Ports) != 1 || portal.Ports[0].HostIP != "127.0.0.1" || portal.Ports[0].Target != 3000 || portal.Ports[0].Published != "3000" {
		t.Fatalf("portal isolation: %+v", portal)
	}
	if _, ok := portal.Networks["portal"]; !ok {
		t.Fatal("portal shares minikube node network")
	}
	if _, ok := config.Services["cloud"].Networks["portal"]; !ok {
		t.Fatal("cloud missing portal network")
	}
	cloud := config.Services["cloud"]
	lab := config.Services["lab"]
	nfs := config.Services["nfs"]
	if len(nfs.CapAdd) != 2 || nfs.CapAdd[0] != "DAC_READ_SEARCH" || nfs.CapAdd[1] != "SYS_ADMIN" || len(nfs.SecurityOpt) != 1 || nfs.SecurityOpt[0] != "apparmor=unconfined" {
		t.Fatalf("excessive NFS privileges: %+v", nfs)
	}
	if nfs.Networks["lab"].IPv4Address != "172.30.0.253" || cloud.Environment["CLOUD_NFS_IP"] != "172.30.0.253" {
		t.Fatal("NFS network mismatch")
	}
	if cloud.Image != "example/cloud:test" || lab.Image != "example/lab:test" {
		t.Fatal("image overrides ignored")
	}
	if cloud.NetworkMode != "" || lab.NetworkMode != "service:cloud" || cloud.User != "1001:1001" || lab.User != "1001:1001" {
		t.Fatalf("network/user: %+v", config.Services)
	}
	network := config.Networks["lab"]
	if network.Name != "minikube-cloud-lab" || network.Driver != "bridge" || len(network.IPAM.Config) != 1 || network.IPAM.Config[0].Subnet != "172.30.0.0/23" || network.IPAM.Config[0].Gateway != "172.30.0.1" {
		t.Fatalf("bridge IPAM: %+v", network)
	}
	if len(cloud.Networks) != 2 || cloud.Networks["lab"].IPv4Address != "172.30.0.254" || len(lab.Networks) != 0 {
		t.Fatalf("shared network: cloud=%+v lab=%+v", cloud.Networks, lab.Networks)
	}
	if cloud.Environment["CLOUD_UBUNTU_IMAGE"] != "example/ubuntu:test" || cloud.Environment["CLOUD_DEBIAN_IMAGE"] != "example/debian:test" {
		t.Fatalf("instance image override ignored: %+v", cloud.Environment)
	}
	if cloud.Environment["CLOUD_DOCKER_NETWORK"] != "minikube-cloud-lab" || cloud.Environment["CLOUD_DOCKER_IP"] != "172.30.0.254" || lab.Environment["KUBECONFIG"] != "/data/minikube/kubeconfig" {
		t.Fatalf("endpoint configuration: cloud=%+v lab=%+v", cloud.Environment, lab.Environment)
	}
	if strings.Join(cloud.Command, " ") != "serve --data-dir /data --api 0.0.0.0:8080" || len(cloud.Ports) != 1 || cloud.Ports[0].HostIP != "127.0.0.1" || cloud.Ports[0].Target != 8080 || cloud.Ports[0].Published != "8080" || len(lab.Ports) != 0 {
		t.Fatalf("localhost publication: command=%v ports=%+v", cloud.Command, cloud.Ports)
	}
	if len(cloud.CapAdd) != 1 || cloud.CapAdd[0] != "DAC_OVERRIDE" {
		t.Fatalf("cloud must safely clean files owned by workloads: %+v", cloud.CapAdd)
	}
	if cloud.Restart != "unless-stopped" || len(cloud.GroupAdd) != 1 || cloud.GroupAdd[0] != "998" {
		t.Fatalf("cloud settings: %+v", cloud)
	}
	if len(lab.Profiles) != 1 || lab.Profiles[0] != "tools" {
		t.Fatalf("tools profile: %+v", lab)
	}
	expected := map[string]string{"/data": "/tmp/cloud-lab-home/.minikube-cloud-lab", "/kube": "/tmp/cloud-lab-home/.kube", "/var/run/docker.sock": "/var/run/docker.sock", "/data/storage": "storage", "/data/instance-disks": "instance-disks"}
	if len(cloud.Volumes) != len(expected) {
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

func TestComposeConfigurationAutoscaling(t *testing.T) {
	for _, file := range []string{"compose.yaml", "compose.rootless.yaml"} {
		restart := "unless-stopped"
		if file == "compose.rootless.yaml" {
			restart = "always"
		}
		for _, enabled := range []bool{false, true} {
			args := []string{"compose", "-f", file}
			if enabled {
				args = append(args, "--profile", "instance-autoscaling")
			}
			args = append(args, "config", "--format", "json")
			cmd := exec.CommandContext(t.Context(), "docker", args...) // #nosec G204 -- test-owned arguments.
			cmd.Env = append(os.Environ(), "UID=1001", "GID=1001", "DOCKER_GID=998", "XDG_RUNTIME_DIR=/run/user/1001",
				"CLOUD_DOCKER_SUBNET=172.29.240.0/23", "CLOUD_DOCKER_GATEWAY=172.29.240.1", "CLOUD_DOCKER_IP=172.29.240.254", "CLOUD_NFS_IP=172.29.240.253", "CLOUD_INSTANCE_LB_IP=172.29.240.255", "CLOUD_INSTANCE_METRICS_IP=172.29.241.0")
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Services map[string]struct {
					Restart  string
					Networks map[string]struct {
						Address string `json:"ipv4_address"`
					}
				}
			}
			if err := json.Unmarshal(out, &config); err != nil {
				t.Fatal(err)
			}
			for svc, ip := range map[string]string{"instance-lb": "172.29.240.255", "instance-metrics": "172.29.241.0", "instance-autoscaler": ""} {
				service, exists := config.Services[svc]
				if exists != enabled || (enabled && (service.Restart != restart || service.Networks["lab"].Address != ip)) {
					t.Errorf("%s profile enabled=%v: %s = %+v", file, enabled, svc, service)
				}
			}
		}
	}
}

func TestOptionalConsoleProfiles(t *testing.T) {
	for _, file := range []string{"compose.yaml", "compose.rootless.yaml"} {
		for _, profile := range []string{"", "monitoring", "portal"} {
			cmdArgs := []string{"compose", "-f", file}
			if profile != "" {
				cmdArgs = append(cmdArgs, "--profile", profile)
			}
			cmdArgs = append(cmdArgs, "config", "--format", "json")
			cmd := exec.CommandContext(t.Context(), "docker", cmdArgs...) // #nosec G204 -- fixed test matrix.
			cmd.Env = append(os.Environ(), "UID=1001", "GID=1001", "DOCKER_GID=998", "XDG_RUNTIME_DIR=/run/user/1001")
			raw, e := cmd.Output()
			if e != nil {
				t.Fatal(e)
			}
			var conf struct {
				Services map[string]struct {
					NetworkMode string `json:"network_mode"`
					Ports       []struct {
						HostIP string `json:"host_ip"`
					}
					Volumes  []struct{ Source string }
					Networks map[string]any
				}
			}
			if e = json.Unmarshal(raw, &conf); e != nil {
				t.Fatal(e)
			}
			for _, name := range []string{"monitoring", "portal", "instance-metrics"} {
				s, exists := conf.Services[name]
				if exists != (profile == name) {
					t.Fatalf("%s with profile %q: unexpected %s", file, profile, name)
				}
				if exists && name == "monitoring" && (s.NetworkMode != "service:cloud" || len(s.Ports) != 0 || len(s.Volumes) != 2) {
					t.Fatalf("collector isolation: %+v", s)
				}
				if exists && name == "portal" && (len(s.Volumes) != 0 || len(s.Ports) != 1 || s.Ports[0].HostIP != "127.0.0.1" || len(s.Networks) != 1) {
					t.Fatalf("portal isolation: %+v", s)
				}
			}
		}
	}
}
