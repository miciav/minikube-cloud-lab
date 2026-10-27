package serviceexposure

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestReconcilePublishesObservedEndpointsAndIsIdempotent(t *testing.T) {
	f, m := newDockerFixture(t)
	svc := testService("one", corev1.ServiceTypeLoadBalancer)
	svc.Annotations = map[string]string{"teacher": "keep", HostEndpointsAnnotation: `[{"hostPort":1}]`}
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "other-provider"}}
	kube := fake.NewSimpleClientset(svc)
	for i := 0; i < 2; i++ {
		if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
			t.Fatal(err)
		}
		got, err := kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var ports []HostPort
		if err := json.Unmarshal([]byte(got.Annotations[HostEndpointsAnnotation]), &ports); err != nil || len(ports) != 1 || ports[0].HostPort != 40080 {
			t.Fatalf("endpoints: %+v %v", ports, err)
		}
		if got.Annotations["teacher"] != "keep" || len(got.Status.LoadBalancer.Ingress) != 2 || got.Status.LoadBalancer.Ingress[1].IP != "172.30.1.1" {
			t.Fatalf("lost status/metadata: %+v", got)
		}
	}
	patches := 0
	for _, a := range kube.Actions() {
		if a.GetVerb() == "patch" {
			patches++
		}
	}
	if patches != 2 || f.created != 1 || f.deleted != 0 {
		t.Fatalf("not idempotent: patches=%d created=%d deleted=%d", patches, f.created, f.deleted)
	}
}

func TestReconcileListFailurePreservesRelays(t *testing.T) {
	f, m := newDockerFixture(t)
	svc := testService("one", corev1.ServiceTypeNodePort)
	if _, err := m.Ensure(t.Context(), "demo", svc, "172.30.0.2"); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("list", "services", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("API down") })
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err == nil || f.deleted != 0 {
		t.Fatalf("outage pruned relays: %v deleted%d", err, f.deleted)
	}
}

func TestReconcileReplacesUIDAndClearsClusterIP(t *testing.T) {
	f, m := newDockerFixture(t)
	svc := testService("old", corev1.ServiceTypeLoadBalancer)
	kube := fake.NewSimpleClientset(svc)
	run := func() {
		t.Helper()
		if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
			t.Fatal(err)
		}
	}
	run()
	if err := kube.CoreV1().Services("lesson").Delete(t.Context(), "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	svc.UID = "new"
	if _, err := kube.CoreV1().Services("lesson").Create(t.Context(), svc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	run()
	if f.created != 2 || f.deleted != 1 {
		t.Fatalf("UID replacement: %d/%d", f.created, f.deleted)
	}
	got, _ := kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
	got.Spec.Type = corev1.ServiceTypeClusterIP
	if _, err := kube.CoreV1().Services("lesson").Update(t.Context(), got, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	run()
	got, _ = kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
	if len(f.objects) != 0 || got.Annotations[HostEndpointsAnnotation] != "" || len(got.Status.LoadBalancer.Ingress) != 0 {
		t.Fatalf("stale exposure: %+v", got)
	}
}

func TestReconcileUnsupportedHasVisibleError(t *testing.T) {
	for _, protocol := range []corev1.Protocol{corev1.ProtocolUDP, corev1.ProtocolSCTP} {
		t.Run(string(protocol), func(t *testing.T) {
			f, m := newDockerFixture(t)
			svc := testService("one", corev1.ServiceTypeLoadBalancer)
			svc.Spec.Ports[0].Protocol = protocol
			kube := fake.NewSimpleClientset(svc)
			if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
				t.Fatal(err)
			}
			got, _ := kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
			if got.Annotations[ExposureErrorAnnotation] == "" || got.Annotations[HostEndpointsAnnotation] != "" || f.created != 0 {
				t.Fatalf("false success: %+v", got)
			}
		})
	}
}

func TestReconcileCanceled(t *testing.T) {
	f, m := newDockerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Reconcile(ctx, fake.NewSimpleClientset(), m, "demo", "172.30.0.2"); !errors.Is(err, context.Canceled) || f.created != 0 {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestReconcileRetriesFailedStatusWithoutRecreatingRelay(t *testing.T) {
	f, m := newDockerFixture(t)
	kube := fake.NewSimpleClientset(testService("one", corev1.ServiceTypeLoadBalancer))
	failed := false
	kube.PrependReactor("patch", "services", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "status" && !failed {
			failed = true
			return true, nil, errors.New("status conflict")
		}
		return false, nil, nil
	})
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err == nil {
		t.Fatal("status failure hidden")
	}
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
		t.Fatal(err)
	}
	if f.created != 1 || f.deleted != 0 {
		t.Fatalf("retry recreated relay: %d/%d", f.created, f.deleted)
	}
}

func TestReconcileCleanupStatusRetryKeepsOwnership(t *testing.T) {
	f, m := newDockerFixture(t)
	kube := fake.NewSimpleClientset(testService("one", corev1.ServiceTypeLoadBalancer))
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
		t.Fatal(err)
	}
	svc, _ := kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
	svc.Spec.Type = corev1.ServiceTypeClusterIP
	if _, err := kube.CoreV1().Services("lesson").Update(t.Context(), svc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	failed := false
	kube.PrependReactor("patch", "services", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "status" && !failed {
			failed = true
			return true, nil, errors.New("status conflict")
		}
		return false, nil, nil
	})
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err == nil {
		t.Fatal("status failure hidden")
	}
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
		t.Fatal(err)
	}
	svc, _ = kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
	if len(svc.Status.LoadBalancer.Ingress) != 0 || len(f.objects) != 0 {
		t.Fatalf("cleanup lost ownership before status retry: %+v", svc.Status)
	}
}

func TestReconcileMultiportAndUnsupportedPolicies(t *testing.T) {
	for _, mode := range []string{"multiport", "Local", "class", "no-nodeports", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			f, m := newDockerFixture(t)
			svc := testService("one", corev1.ServiceTypeLoadBalancer)
			switch mode {
			case "multiport":
				svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: "tls", Protocol: corev1.ProtocolTCP, Port: 443, NodePort: 30443})
			case "Local":
				svc.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyLocal
			case "class":
				value := "other"
				svc.Spec.LoadBalancerClass = &value
			case "no-nodeports":
				value := false
				svc.Spec.AllocateLoadBalancerNodePorts = &value
			case "mixed":
				svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: "dns", Protocol: corev1.ProtocolUDP, Port: 53, NodePort: 30053})
			}
			kube := fake.NewSimpleClientset(svc)
			if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
				t.Fatal(err)
			}
			got, _ := kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
			if mode == "multiport" {
				var ports []HostPort
				if err := json.Unmarshal([]byte(got.Annotations[HostEndpointsAnnotation]), &ports); err != nil || len(ports) != 2 {
					t.Fatalf("multiport: %v %v", ports, err)
				}
			} else if got.Annotations[ExposureErrorAnnotation] == "" || f.created != 0 {
				t.Fatalf("unsupported service published: %+v", got)
			}
		})
	}
}

func TestReconcilePreservesForeignIngressOnUnsupportedService(t *testing.T) {
	_, m := newDockerFixture(t)
	svc := testService("one", corev1.ServiceTypeLoadBalancer)
	svc.Spec.Ports[0].Protocol = corev1.ProtocolUDP
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "other-provider"}, {IP: "192.0.2.10"}}
	kube := fake.NewSimpleClientset(svc)
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
		t.Fatal(err)
	}
	got, _ := kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
	if len(got.Status.LoadBalancer.Ingress) != 2 {
		t.Fatalf("foreign status overwritten: %+v", got.Status)
	}
}

func TestReconcileGuardsWritesAgainstReplacedService(t *testing.T) {
	_, m := newDockerFixture(t)
	svc := testService("old", corev1.ServiceTypeLoadBalancer)
	svc.ResourceVersion = "7"
	kube := fake.NewSimpleClientset(svc)
	checked := false
	kube.PrependReactor("patch", "services", func(a ktesting.Action) (bool, runtime.Object, error) {
		var patch struct {
			Metadata struct {
				UID     string `json:"uid"`
				Version string `json:"resourceVersion"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(a.(ktesting.PatchAction).GetPatch(), &patch); err != nil {
			t.Fatal(err)
		}
		if patch.Metadata.UID != "old" || patch.Metadata.Version != "7" {
			t.Fatalf("unguarded write: %+v", patch)
		}
		checked = true
		return true, nil, errors.New("Service replaced: UID/resourceVersion conflict")
	})
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err == nil || !checked {
		t.Fatalf("replacement conflict: %v checked%v", err, checked)
	}
	got, _ := kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
	if len(got.Annotations) != 0 || len(got.Status.LoadBalancer.Ingress) != 0 {
		t.Fatal("failed write published endpoints")
	}
}

func TestReconcileServicePortChangeAndDeletion(t *testing.T) {
	f, m := newDockerFixture(t)
	kube := fake.NewSimpleClientset(testService("one", corev1.ServiceTypeNodePort))
	run := func() {
		t.Helper()
		if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
			t.Fatal(err)
		}
	}
	run()
	svc, _ := kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
	svc.Spec.Type = corev1.ServiceTypeLoadBalancer
	svc.Spec.Ports[0].Port = 8080
	if _, err := kube.CoreV1().Services("lesson").Update(t.Context(), svc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	run()
	if f.created != 2 || f.deleted != 1 || f.objects["proxy-2"].HostConfig.PortBindings["8080/tcp"] == nil {
		t.Fatalf("port/type mutation did not converge: %d/%d", f.created, f.deleted)
	}
	if err := kube.CoreV1().Services("lesson").Delete(t.Context(), "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	run()
	if len(f.objects) != 0 {
		t.Fatal("deleted Service leaked a relay")
	}
}
