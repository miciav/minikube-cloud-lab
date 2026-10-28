package cloud

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

type staticStorage struct{}

func (staticStorage) Ensure(context.Context, string, int64) error { return nil }
func (staticStorage) Delete(context.Context, string) error        { return nil }
func staticService(t *testing.T) (*Service, Volume) {
	t.Helper()
	s, err := NewService(fake.New(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureStorage(staticStorage{}, "1Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	s.state.Clusters = []Cluster{{Name: "demo", Status: "RUNNING"}}
	v, err := s.CreateAvailableVolume("64Mi")
	if err != nil {
		t.Fatal(err)
	}
	return s, v
}

func staticClient() *kfake.Clientset {
	client := kfake.NewSimpleClientset()
	client.PrependReactor("create", "persistentvolumes", func(a ktesting.Action) (bool, runtime.Object, error) {
		pv := a.(ktesting.CreateAction).GetObject().(*corev1.PersistentVolume)
		pv.UID = "pv-identity"
		pv.ResourceVersion = "1"
		return false, nil, nil
	})
	return client
}

func TestStaticPublicationBindingRetentionAndUnpublish(t *testing.T) {
	s, v := staticService(t)
	ctx := context.Background()
	client := staticClient()
	if err := s.reservePublication(v.ID, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := s.syncPublication(ctx, client, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.syncPublication(ctx, client, v.ID); err != nil {
		t.Fatal(err)
	}
	pv, err := client.CoreV1().PersistentVolumes().Get(ctx, v.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Spec.ClaimRef != nil || pv.Spec.StorageClassName != "cloud-static" || pv.Spec.PersistentVolumeReclaimPolicy != "Retain" {
		t.Fatalf("not a static PV: %+v", pv.Spec)
	}
	if err = s.DeleteVolume(v.ID); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("deleted published volume %v", err)
	}
	class := "cloud-static"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default", UID: "claim-id"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: v.ID, StorageClassName: &class, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("64Mi")}}}}
	if _, err = client.CoreV1().PersistentVolumeClaims("default").Create(ctx, claim, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pv.Spec.ClaimRef = &corev1.ObjectReference{Namespace: "default", Name: "data", UID: claim.UID}
	pv.Status.Phase = corev1.VolumeBound
	if _, err = client.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileStorage(ctx, client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes()[0]; got.Status != "Bound" || got.ClaimUID != "claim-id" {
		t.Fatalf("binding absent %+v", got)
	}
	if err = s.unpublishVolume(ctx, client, v.ID); err == nil {
		t.Fatal("unpublished bound volume")
	}
	if err = client.CoreV1().PersistentVolumeClaims("default").Delete(ctx, "data", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	pv.Status.Phase = corev1.VolumeReleased
	if _, err = client.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileStorage(ctx, client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if s.Volumes()[0].Status != "Released" {
		t.Fatal("released PV lost publication")
	}
	if err = s.unpublishVolume(ctx, client, v.ID); err != nil {
		t.Fatal(err)
	}
	if s.Volumes()[0].Status != "Available" {
		t.Fatal("volume not available after unpublish")
	}
	if err = s.DeleteVolume(v.ID); err != nil {
		t.Fatal(err)
	}
}

func TestStaticClaimDoesNotAllocateAndForeignPVIsProtected(t *testing.T) {
	s, v := staticService(t)
	ctx := context.Background()
	class := "cloud-static"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "waiting", Namespace: "default", UID: "waiting"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class}}
	client := kfake.NewSimpleClientset(claim)
	if err := s.ReconcileStorage(ctx, client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if len(s.Volumes()) != 1 {
		t.Fatal("static claim dynamically allocated")
	}
	if err := s.reservePublication(v.ID, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().PersistentVolumes().Create(ctx, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID, UID: "foreign"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncPublication(ctx, client, v.ID); err == nil {
		t.Fatal("accepted foreign PV")
	}
	if err := s.unpublishVolume(ctx, client, v.ID); err == nil {
		t.Fatal("removed foreign PV")
	}
}

func TestStaticClusterDeletionReleasesUnboundPublication(t *testing.T) {
	s, v := staticService(t)
	if err := s.reservePublication(v.ID, "demo"); err != nil {
		t.Fatal(err)
	}
	op, err := s.DeleteCluster("demo", "test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Wait(context.Background(), op.ID)
	if err != nil || result.Status != "DONE" {
		t.Fatalf("delete %+v %v", result, err)
	}
	got := s.Volumes()[0]
	if got.Status != "Available" || got.Cluster != "" {
		t.Fatalf("publication stranded: %+v", got)
	}
	status, err := s.StorageCapacity()
	if err != nil || status.Allocated != "64Mi" {
		t.Fatalf("retained budget %+v %v", status, err)
	}
}
