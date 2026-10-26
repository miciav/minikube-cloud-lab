//go:build integration

package minikube_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
	backend "github.com/miciav/minikube-cloud-lab/internal/infra/minikube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	mkconfig "k8s.io/minikube/pkg/minikube/config"
)

// Mark this as an integration run so upstream permits real binary downloads.
func init() { flag.String("minikube-start-args", "", "minikube integration test marker") }

// Without a minikube executable, the real library must still manage the entire lifecycle.
func TestClusterLifecycle(t *testing.T) {
	path := t.TempDir()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() && e.Name() != "minikube" {
				_ = os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(path, e.Name()))
			}
		}
	}
	t.Setenv("PATH", path)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "config"))
	if _, err := exec.LookPath("minikube"); err == nil {
		t.Fatal("minikube must be absent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	inf, err := backend.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("it-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if err := inf.DeleteCluster(context.Background(), name); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	labels := map[string]string{
		"node.kubernetes.io/instance-type": "lab-small",
		"topology.kubernetes.io/region":    "lab-region-1",
		"topology.kubernetes.io/zone":      "lab-region-1-a",
	}
	if err := inf.CreateCluster(ctx, infra.ClusterSpec{Name: name, KubernetesVersion: "v1.35.6", Shape: infra.MachineShape{CPUs: 2, MemoryMiB: 2048}, NodeLabels: labels}); err != nil {
		t.Fatal(err)
	}
	machines, err := inf.Machines(ctx, name)
	if err != nil || len(machines) != 1 || !machines[0].ControlPlane || !machines[0].Running {
		t.Fatalf("initial machines: %+v, %v", machines, err)
	}
	kubeconfig, err := inf.Kubeconfig(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.Load(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, cluster := range cfg.Clusters {
		if len(cluster.CertificateAuthorityData) == 0 || cluster.CertificateAuthority != "" {
			t.Fatal("CA must be embedded")
		}
	}
	for _, auth := range cfg.AuthInfos {
		if len(auth.ClientCertificateData) == 0 || len(auth.ClientKeyData) == 0 {
			t.Fatal("client identity must be embedded")
		}
	}
	rest, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(rest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("yakd-dashboard").Get(ctx, "yakd-dashboard", metav1.GetOptions{}); err != nil {
		t.Fatalf("YAKD must be installed automatically: %v", err)
	}
	profile, err := mkconfig.Load(name)
	if err != nil || !profile.Addons["yakd"] {
		t.Fatalf("YAKD not recorded as enabled: %+v, %v", profile, err)
	}
	yakdDeadline := time.Now().Add(5 * time.Minute)
	for {
		deploy, err := client.AppsV1().Deployments("yakd-dashboard").Get(ctx, "yakd-dashboard", metav1.GetOptions{})
		if err == nil && deploy.Status.AvailableReplicas == 1 {
			break
		}
		if time.Now().After(yakdDeadline) || ctx.Err() != nil {
			t.Fatalf("YAKD never became available: %+v, %v", deploy, err)
		}
		time.Sleep(time.Second)
	}
	page, err := client.CoreV1().Services("yakd-dashboard").ProxyGet("http", "yakd-dashboard", "80", "", nil).DoRaw(ctx)
	if err != nil || !strings.Contains(strings.ToLower(string(page)), "<html") {
		t.Fatalf("YAKD dashboard did not serve HTML: %v", err)
	}
	t.Log("automatic YAKD addon available; dashboard serves HTML")
	var capacity int64
	assertNodes := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Minute)
		for {
			nodes, listErr := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			ready := 0
			if listErr == nil {
				for _, n := range nodes.Items {
					for k, v := range labels {
						if n.Labels[k] != v {
							t.Fatalf("node %s label %s: %q", n.Name, k, n.Labels[k])
						}
					}
					if capacity == 0 {
						capacity = n.Status.Capacity.Cpu().Value()
					}
					if n.Status.Capacity.Cpu().Value() != capacity {
						t.Fatalf("different node CPU capacities: %s", n.Name)
					}
					t.Logf("node %s capacity=%v allocatable=%v", n.Name, n.Status.Capacity, n.Status.Allocatable)
					if n.Status.Allocatable.Memory().Value() != 2147483648 {
						t.Fatalf("node %s allocatable memory: %s", n.Name, n.Status.Allocatable.Memory())
					}
					if n.Status.Allocatable.Cpu().Value() != 2 {
						t.Fatalf("node %s allocatable CPU: %s", n.Name, n.Status.Allocatable.Cpu())
					}
					for _, c := range n.Status.Conditions {
						if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
							ready++
						}
					}
				}
				if len(nodes.Items) == want && ready == want {
					return
				}
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				t.Fatalf("want %d Ready nodes, got %d, error %v", want, ready, listErr)
			}
			time.Sleep(2 * time.Second)
		}
	}

	assertShape := func(machine string) {
		t.Helper()
		data, err := exec.CommandContext(ctx, "docker", "inspect", machine, "--format", "{{json .HostConfig}}").Output()
		if err != nil {
			t.Fatal(err)
		}
		var config struct{ NanoCpus, Memory int64 }
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		if config.NanoCpus != 2000000000 || config.Memory != 2147483648 {
			t.Fatalf("container shape: %+v", config)
		}
	}
	assertShape(machines[0].Name)
	assertNodes(1)
	dnsDeadline := time.Now().Add(time.Minute)
	for {
		dns, err := client.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
		if err == nil && dns.Status.AvailableReplicas > 0 {
			break
		}
		if time.Now().After(dnsDeadline) {
			t.Fatalf("CoreDNS never became available: %+v, %v", dns, err)
		}
		time.Sleep(time.Second)
	}
	_, err = client.CoreV1().Pods("default").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "dns-check"}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "lookup", Image: "busybox:1.37.0", Command: []string{"sh", "-c", "nslookup kubernetes.default.svc.cluster.local && nslookup host.minikube.internal"}}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	probeDeadline := time.Now().Add(time.Minute)
	for {
		pod, err := client.CoreV1().Pods("default").Get(ctx, "dns-check", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if pod.Status.Phase == corev1.PodSucceeded {
			break
		}
		if pod.Status.Phase == corev1.PodFailed || time.Now().After(probeDeadline) {
			t.Fatalf("DNS probe failed: %+v", pod.Status)
		}
		time.Sleep(time.Second)
	}
	worker, err := inf.AddMachine(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if worker.ControlPlane || !worker.Running {
		t.Fatalf("worker: %+v", worker)
	}
	machines, err = inf.Machines(ctx, name)
	if err != nil || len(machines) != 2 {
		t.Fatalf("after add: %+v, %v", machines, err)
	}
	assertShape(worker.Name)
	assertNodes(2)
	if err := inf.DeleteMachine(ctx, name, worker.Name); err != nil {
		t.Fatal(err)
	}
	machines, err = inf.Machines(ctx, name)
	if err != nil || len(machines) != 1 {
		t.Fatalf("after delete: %+v, %v", machines, err)
	}
	assertNodes(1)
	if err := inf.DeleteCluster(ctx, name); err != nil {
		t.Fatal(err)
	}
	machines, err = inf.Machines(ctx, name)
	if err == nil && len(machines) != 0 {
		t.Fatalf("machines remain: %+v", machines)
	}
	output, err := exec.CommandContext(ctx, "docker", "ps", "-a", "--filter", "name="+name, "--format", "{{.Names}}").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("containers remain: %s (%v)", output, err)
	}
}
