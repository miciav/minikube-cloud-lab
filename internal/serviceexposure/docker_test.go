package serviceexposure

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/docker/docker/api/types/container"

	"github.com/docker/docker/api/types/network"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testService(uid string, kind corev1.ServiceType) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "lesson", UID: types.UID(uid)}, Spec: corev1.ServiceSpec{Type: kind, Ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 80, NodePort: 30080}}}}
}

func TestDockerNodePortUsesExactLocalhostPort(t *testing.T) {
	f, m := newDockerFixture(t)
	status, err := m.Ensure(t.Context(), "demo", testService("one", corev1.ServiceTypeNodePort), "172.30.0.2")
	if err != nil || len(status.Ports) != 1 || status.Ports[0].HostPort != 30080 {
		t.Fatalf("NodePort endpoint: %+v %v", status, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created != 1 {
		t.Fatalf("created %d relays", f.created)
	}
	v := f.objects["proxy-1"]
	if v == nil || v.ip != "172.30.1.1" || v.Image != "sha256:relay-image" || len(v.HostConfig.Binds) != 0 || len(v.HostConfig.Mounts) != 0 {
		t.Fatalf("unsafe or missing relay: %+v", v)
	}
	b := v.HostConfig.PortBindings["30080/tcp"]
	if len(b) != 1 || b[0].HostIP != "127.0.0.1" || b[0].HostPort != "30080" {
		t.Fatalf("wrong host binding: %+v", b)
	}
	if v.Labels["created_by.minikube.sigs.k8s.io"] != "" || v.Labels["name.minikube.sigs.k8s.io"] != "" {
		t.Fatal("relay must not be registered as a minikube machine")
	}
	if v.Labels["minikube-cloud-lab.cluster"] != "demo" || v.Labels["minikube-cloud-lab.service-uid"] != "one" {
		t.Fatalf("missing ownership: %v", v.Labels)
	}
	var forwards []Forward
	if len(v.Cmd) != 3 || v.Cmd[0] != "service-proxy" || v.Cmd[1] != "--config-json" {
		t.Fatalf("relay command: %v", v.Cmd)
	}
	if err := json.Unmarshal([]byte(v.Cmd[2]), &forwards); err != nil || len(forwards) != 1 || forwards[0].Target != "172.30.0.2:30080" {
		t.Fatalf("wrong TCP forwarding: %+v %v", forwards, err)
	}
}

func TestDockerLoadBalancersShareServicePortWithoutHostConflict(t *testing.T) {
	f, m := newDockerFixture(t)
	for _, tc := range []struct {
		uid, ip string
		port    int32
	}{{"one", "172.30.1.1", 40080}, {"two", "172.30.1.2", 40081}} {
		svc := testService(tc.uid, corev1.ServiceTypeLoadBalancer)
		status, err := m.Ensure(t.Context(), "demo", svc, "172.30.0.2")
		if err != nil || status.IP != tc.ip || len(status.Ports) != 1 || status.Ports[0].HostPort != tc.port {
			t.Fatalf("LB endpoint: %+v %v", status, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.objects {
		b := v.HostConfig.PortBindings["80/tcp"]
		if len(b) != 1 || b[0].HostIP != "127.0.0.1" || b[0].HostPort != "" {
			t.Fatalf("LB must request a free host port for service port 80: %+v", b)
		}
		var forwards []Forward
		if err := json.Unmarshal([]byte(v.Cmd[2]), &forwards); err != nil || len(forwards) != 1 || forwards[0].ListenPort != 80 || forwards[0].Target != "172.30.0.2:30080" {
			t.Fatalf("LB IP must listen on service port 80: %+v %v", forwards, err)
		}
	}
}

func TestDockerRepeatedReconcileReusesRelay(t *testing.T) {
	f, m := newDockerFixture(t)
	svc := testService("one", corev1.ServiceTypeLoadBalancer)
	first, err := m.Ensure(t.Context(), "demo", svc, "172.30.0.2")
	if err != nil {
		t.Fatal(err)
	}
	m = NewManager(f.client, "lab-net", "sha256:relay-image")
	second, err := m.Ensure(t.Context(), "demo", svc, "172.30.0.2")
	if err != nil || len(first.Ports) != 1 || len(second.Ports) != 1 || first.Ports[0].HostPort != second.Ports[0].HostPort {
		t.Fatalf("unstable endpoint: %+v %+v %v", first, second, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created != 1 || f.deleted != 0 {
		t.Fatalf("identical service recreated relay: %d/%d", f.created, f.deleted)
	}
}

func TestDockerConflictAndUnsafeReadbackNeverPublishEndpoint(t *testing.T) {
	for _, mode := range []string{"occupied", "wildcard"} {
		t.Run(mode, func(t *testing.T) {
			f, m := newDockerFixture(t)
			f.conflict = mode == "occupied"
			f.wildcard = mode == "wildcard"
			status, err := m.Ensure(t.Context(), "demo", testService("one", corev1.ServiceTypeNodePort), "172.30.0.2")
			if err == nil || len(status.Ports) != 0 {
				t.Fatalf("false healthy endpoint: %+v %v", status, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.objects) != 0 {
				t.Fatalf("failed relay leaked: %v", f.objects)
			}
		})
	}
}

func TestDockerPoolAndNetworkValidation(t *testing.T) {
	for _, mode := range []string{"old-subnet", "missing-gateway", "exhausted", "wrong-target"} {
		t.Run(mode, func(t *testing.T) {
			f, m := newDockerFixture(t)
			target := "172.30.0.2"
			switch mode {
			case "old-subnet":
				f.network.IPAM.Config[0].Subnet = "172.30.0.0/24"
			case "missing-gateway":
				f.network.IPAM.Config[0].Gateway = ""
			case "wrong-target":
				target = "10.1.0.2"
			case "exhausted":
				for i := 1; i <= 254; i++ {
					f.network.Containers[fmt.Sprint(i)] = network.EndpointResource{IPv4Address: fmt.Sprintf("172.30.1.%d/23", i)}
				}
			}
			if _, err := m.Ensure(t.Context(), "demo", testService("one", corev1.ServiceTypeLoadBalancer), target); err == nil {
				t.Fatal("invalid network/pool accepted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.created != 0 {
				t.Fatalf("created relay before validating network: %d", f.created)
			}
		})
	}
}

func TestDockerCleanupHonorsOwnershipAndErrors(t *testing.T) {
	f, m := newDockerFixture(t)
	for _, uid := range []string{"one", "two"} {
		if _, err := m.Ensure(t.Context(), "demo", testService(uid, corev1.ServiceTypeLoadBalancer), "172.30.0.2"); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	f.objects["foreign"] = &dockerRecord{Config: container.Config{Labels: map[string]string{"minikube-cloud-lab.service-proxy": "true", "minikube-cloud-lab.cluster": "another", "minikube-cloud-lab.service-uid": "one"}}, id: "foreign"}
	f.objects["neighbor"] = &dockerRecord{Config: container.Config{Labels: map[string]string{"minikube-cloud-lab.service-proxy": "true", "minikube-cloud-lab.cluster": "demo", "minikube-cloud-lab.service-uid": "neighbor", "minikube-cloud-lab.network": "another-net"}}, id: "neighbor"}
	f.removeFailure = true
	f.mu.Unlock()
	if err := m.Remove(t.Context(), "demo", "one"); err == nil {
		t.Fatal("failed removal reported success")
	}
	f.mu.Lock()
	f.removeFailure = false
	f.mu.Unlock()
	if err := m.Prune(t.Context(), "demo", []types.UID{"two"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	remaining := len(f.objects)
	_, foreign := f.objects["foreign"]
	f.mu.Unlock()
	if remaining != 3 || !foreign {
		t.Fatalf("prune ownership mismatch: remaining=%d foreign=%v", remaining, foreign)
	}
	if err := m.DeleteCluster(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.objects) != 2 || f.objects["foreign"] == nil || f.objects["neighbor"] == nil {
		t.Fatal("cleanup deleted foreign resource or leaked owned relay")
	}
}

func TestDockerRejectsUnsupportedServiceBeforeAllocation(t *testing.T) {
	for _, mode := range []string{"ClusterIP", "UDP", "SCTP", "Local", "class", "no-nodeports"} {
		t.Run(mode, func(t *testing.T) {
			f, m := newDockerFixture(t)
			svc := testService("one", corev1.ServiceTypeLoadBalancer)
			switch mode {
			case "ClusterIP":
				svc.Spec.Type = corev1.ServiceTypeClusterIP
			case "UDP":
				svc.Spec.Ports[0].Protocol = corev1.ProtocolUDP
			case "SCTP":
				svc.Spec.Ports[0].Protocol = corev1.ProtocolSCTP
			case "Local":
				svc.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyLocal
			case "class":
				value := "another/provider"
				svc.Spec.LoadBalancerClass = &value
			case "no-nodeports":
				value := false
				svc.Spec.AllocateLoadBalancerNodePorts = &value
			}
			if _, err := m.Ensure(t.Context(), "demo", svc, "172.30.0.2"); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unsupported %s accepted: %v", mode, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.created != 0 {
				t.Fatal("unsupported service created a relay")
			}
		})
	}
}

func TestDockerCustomNetworkAvoidsOccupiedRelayAddress(t *testing.T) {
	f, m := newDockerFixture(t)
	f.network.IPAM.Config[0] = network.IPAMConfig{Subnet: "172.29.240.0/23", Gateway: "172.29.240.1"}
	f.network.Containers = map[string]network.EndpointResource{"node": {IPv4Address: "172.29.240.2/23"}, "cloud": {IPv4Address: "172.29.240.254/23"}, "occupied": {IPv4Address: "172.29.241.1/23"}}
	status, err := m.Ensure(t.Context(), "demo", testService("one", corev1.ServiceTypeLoadBalancer), "172.29.240.2")
	if err != nil || status.IP != "172.29.241.2" {
		t.Fatalf("custom relay pool: %+v %v", status, err)
	}
}

func TestDockerMultiplePortsAndStoppedRelay(t *testing.T) {
	f, m := newDockerFixture(t)
	svc := testService("one", corev1.ServiceTypeLoadBalancer)
	svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: "https", Protocol: corev1.ProtocolTCP, Port: 443, NodePort: 30443})
	first, err := m.Ensure(t.Context(), "demo", svc, "172.30.0.2")
	if err != nil || len(first.Ports) != 2 || first.Ports[0].HostPort == first.Ports[1].HostPort {
		t.Fatalf("multiport endpoints: %+v %v", first, err)
	}
	f.mu.Lock()
	f.objects["proxy-1"].running = false
	f.mu.Unlock()
	second, err := m.Ensure(t.Context(), "demo", svc, "172.30.0.2")
	if err != nil || len(second.Ports) != 2 || second.Ports[0].HostPort != first.Ports[0].HostPort || second.Ports[1].HostPort != first.Ports[1].HostPort {
		t.Fatalf("restart changed endpoints: %+v %v", second, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created != 1 {
		t.Fatal("stopped relay recreated")
	}
}

func TestDockerErrorsReturnNoEndpointAndCleanCandidates(t *testing.T) {
	for _, path := range []string{"/networks/lab-net", "/containers/json", "/containers/create", "/containers/proxy-1/json"} {
		t.Run(path, func(t *testing.T) {
			f, m := newDockerFixture(t)
			f.failPath = path
			status, err := m.Ensure(t.Context(), "demo", testService("one", corev1.ServiceTypeLoadBalancer), "172.30.0.2")
			if err == nil || status.IP != "" || len(status.Ports) != 0 {
				t.Fatalf("Docker failure reported endpoint: %+v %v", status, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.objects) != 0 {
				t.Fatal("failed request left candidate behind")
			}
		})
	}
}
