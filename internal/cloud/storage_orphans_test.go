package cloud

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

type flakyStorage struct{ fail bool }

func (b *flakyStorage) Ensure(context.Context, string, int64) error {
	if b.fail {
		return errors.New("storage offline")
	}
	return nil
}
func (b *flakyStorage) Delete(context.Context, string) error { return nil }

func flakyService(t *testing.T) (*Service, *flakyStorage) {
	t.Helper()
	s, err := NewService(fake.New(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &flakyStorage{fail: true}
	if err = s.ConfigureStorage(b, "1Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	return s, b
}

func TestFailedUnclaimedAllocationReleasesBudget(t *testing.T) {
	s, b := flakyService(t)
	if _, err := s.CreateAvailableVolume("512Mi"); err == nil {
		t.Fatal("failed backend accepted")
	}
	b.fail = false
	s.retryVolumeDeletions()
	if v := s.Volumes(); len(v) != 0 {
		t.Fatalf("abandoned reservation kept: %+v", v)
	}
	if _, err := s.CreateAvailableVolume("1Gi"); err != nil {
		t.Fatalf("budget not released: %v", err)
	}
}

func TestCreatingVolumeIsReleasedWhenClaimDisappears(t *testing.T) {
	s, b := flakyService(t)
	class := "cloud-standard"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default", UID: "uid-1"}, Spec: corev1.PersistentVolumeClaimSpec{
		StorageClassName: &class, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("64Mi")}},
	}}
	client := kfake.NewSimpleClientset(claim)
	ctx := context.Background()
	if err := s.ReconcileStorage(ctx, client, "demo", "172.30.0.253"); err == nil {
		t.Fatal("failed backend accepted")
	}
	s.retryVolumeDeletions()
	if v := s.Volumes(); len(v) != 1 || v[0].Status != "Creating" {
		t.Fatalf("claimed reservation must wait for its claim: %+v", v)
	}
	if err := client.CoreV1().PersistentVolumeClaims("default").Delete(ctx, "data", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	b.fail = false
	if err := s.ReconcileStorage(ctx, client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if v := s.Volumes(); len(v) != 0 {
		t.Fatalf("orphaned reservation kept: %+v", v)
	}
}

func TestStartupDefersBackendDeletionsUntilStorageIsConfigured(t *testing.T) {
	dir := t.TempDir()
	st := snapshot{Clusters: []Cluster{}, Machines: []Machine{}, Operations: []Operation{}, Volumes: []Volume{{ID: "pvc-pending", Capacity: "64Mi", Policy: "Retain", Status: "Deleting", Enforced: true}}}
	if err := save(dir, st); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	if _, err := NewService(fake.New(), dir, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("spurious startup error: %s", logs.String())
	}
}
