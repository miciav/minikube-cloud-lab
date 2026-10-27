package serviceexposure

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestDockerTransientInspectPreservesExistingRelay(t *testing.T) {
	f, m := newDockerFixture(t)
	svc := testService("one", corev1.ServiceTypeLoadBalancer)
	before, err := m.Ensure(t.Context(), "demo", svc, "172.30.0.2")
	if err != nil {
		t.Fatal(err)
	}
	f.failPath = "/containers/proxy-1/json"
	if _, err = m.Ensure(t.Context(), "demo", svc, "172.30.0.2"); err == nil {
		t.Fatal("expected inspection failure")
	}
	f.failPath = ""
	after, err := m.Ensure(t.Context(), "demo", svc, "172.30.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if f.created != 1 || f.deleted != 0 || before.Ports[0].HostPort != after.Ports[0].HostPort {
		t.Fatalf("transient GET deleted healthy relay: created=%d deleted=%d hostPort=%d->%d", f.created, f.deleted, before.Ports[0].HostPort, after.Ports[0].HostPort)
	}
}

func TestReconcileChangedLBWithFailedStatusConverges(t *testing.T) {
	f, m := newDockerFixture(t)
	placeholder := testService("placeholder", corev1.ServiceTypeLoadBalancer)
	placeholder.Name = "placeholder"
	if _, err := m.Ensure(t.Context(), "demo", placeholder, "172.30.0.2"); err != nil {
		t.Fatal(err)
	}
	svc := testService("one", corev1.ServiceTypeLoadBalancer)
	kube := fake.NewSimpleClientset(placeholder, svc)
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
		t.Fatal(err)
	}
	if err := kube.CoreV1().Services("lesson").Delete(t.Context(), "placeholder", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	svc, err := kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	svc.Spec.Ports[0].Port = 8080
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
		t.Fatal("expected status conflict")
	}
	if f.objects["proxy-2"] == nil || f.created != 2 || f.deleted != 1 {
		t.Fatal("failed status cleanup discarded the old relay before ownership was cleared")
	}
	if err := Reconcile(t.Context(), kube, m, "demo", "172.30.0.2"); err != nil {
		t.Fatal(err)
	}
	svc, err = kube.CoreV1().Services("lesson").Get(t.Context(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.Status.LoadBalancer.Ingress) != 1 {
		t.Fatalf("orphaned ingress survives retries: %+v; actual relay %s", svc.Status.LoadBalancer.Ingress, f.objects["proxy-3"].ip)
	}
}
