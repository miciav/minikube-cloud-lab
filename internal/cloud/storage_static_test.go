package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

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
	if err := s.reservePublication(v.ID, "demo", nil); err != nil {
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
	if err := s.reservePublication(v.ID, "demo", nil); err != nil {
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
	if err := s.reservePublication(v.ID, "demo", nil); err != nil {
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

func TestStaticPublicOperationsRecoverCancelledPublication(t *testing.T) {
	s, v := staticService(t)
	var pv *corev1.PersistentVolume
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			if pv != nil {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Failure", Reason: metav1.StatusReasonAlreadyExists, Code: 409})
				return
			}
			pv = &corev1.PersistentVolume{}
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				t.Error(readErr)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if err := runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), body, pv); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			pv.UID = "public-pv"
			pv.ResourceVersion = "1"
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(pv)
		case http.MethodGet:
			if pv == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404})
				return
			}
			_ = json.NewEncoder(w).Encode(pv)
		case http.MethodDelete:
			pv = nil
			_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Success"})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	cfg := clientapi.NewConfig()
	cfg.Clusters["demo"] = &clientapi.Cluster{Server: server.URL}
	cfg.AuthInfos["demo"] = &clientapi.AuthInfo{}
	cfg.Contexts["demo"] = &clientapi.Context{Cluster: "demo", AuthInfo: "demo"}
	cfg.CurrentContext = "demo"
	dir := filepath.Join(s.dir, "minikube")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := clientcmd.WriteToFile(*cfg, filepath.Join(dir, "kubeconfig")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.PublishVolume(ctx, v.ID, "demo"); err == nil {
		t.Fatal("cancelled publication succeeded")
	}
	if s.Volumes()[0].Status != "Publishing" {
		t.Fatal("exclusive intent not preserved")
	}
	if err := s.PublishVolume(context.Background(), v.ID, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishVolume(context.Background(), v.ID, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnpublishVolume(context.Background(), v.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UnpublishVolume(context.Background(), v.ID); err != nil {
		t.Fatal(err)
	}
	if s.Volumes()[0].Status != "Available" {
		t.Fatal("public unpublish did not preserve reusable storage")
	}
}

func TestStaticUnpublishWaitsForProtectionController(t *testing.T) {
	s, v := staticService(t)
	client := staticClient()
	ctx := context.Background()
	if err := s.reservePublication(v.ID, "demo", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.syncPublication(ctx, client, v.ID); err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("delete", "persistentvolumes", func(_ ktesting.Action) (bool, runtime.Object, error) {
		go func() {
			time.Sleep(100 * time.Millisecond)
			_ = client.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("persistentvolumes"), "", v.ID)
		}()
		return true, nil, nil
	})
	if err := s.unpublishVolume(ctx, client, v.ID); err != nil {
		t.Fatalf("protection cleanup not awaited: %v", err)
	}
	if s.Volumes()[0].Status != "Available" {
		t.Fatal("publication not safely released")
	}
}

func TestStaticUnpublishRejectsReplacedIdentityAndUnavailableAPI(t *testing.T) {
	s, v := staticService(t)
	client := staticClient()
	ctx := context.Background()
	if err := s.reservePublication(v.ID, "demo", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.syncPublication(ctx, client, v.ID); err != nil {
		t.Fatal(err)
	}
	pv, err := client.CoreV1().PersistentVolumes().Get(ctx, v.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pv.UID = "replacement"
	if _, err = client.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = s.unpublishVolume(ctx, client, v.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("replaced PV accepted %v", err)
	}
	pv.UID = "pv-identity"
	pv.ResourceVersion = ""
	if _, err = client.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = s.unpublishVolume(ctx, client, v.ID); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("missing preconditions accepted %v", err)
	}
	client.PrependReactor("get", "persistentvolumes", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("API unavailable") })
	if err = s.unpublishVolume(ctx, client, v.ID); err == nil {
		t.Fatal("API failure accepted")
	}
	if got := s.Volumes()[0]; got.Status != "Published" {
		t.Fatalf("unsafe release: %+v", got)
	}
}

func TestStaticOperatorFailuresPreserveAllocation(t *testing.T) {
	s, v := staticService(t)
	ctx := context.Background()
	if err := s.PublishVolume(ctx, "pvc-missing", "demo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing volume %v", err)
	}
	if err := s.UnpublishVolume(ctx, "pvc-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing volume %v", err)
	}
	if err := s.PublishVolume(ctx, v.ID, "demo"); err == nil {
		t.Fatal("missing Kubernetes configuration accepted")
	}
	if err := s.UnpublishVolume(ctx, v.ID); err == nil {
		t.Fatal("unpublished without inspecting API")
	}
	s.state.Clusters[0].Status = "ERROR"
	if err := s.UnpublishVolume(ctx, v.ID); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("unavailable cluster %v", err)
	}
	if got := s.Volumes()[0]; got.Status != "Publishing" {
		t.Fatalf("lost exclusive allocation %+v", got)
	}
}

func TestStaticPublishCannotRecreatePreviouslyOwnedPV(t *testing.T) {
	s, v := staticService(t)
	client := staticClient()
	ctx := context.Background()
	if err := s.reservePublication(v.ID, "demo", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.syncPublication(ctx, client, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.CoreV1().PersistentVolumes().Delete(ctx, v.ID, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("create", "persistentvolumes", func(a ktesting.Action) (bool, runtime.Object, error) {
		pv := a.(ktesting.CreateAction).GetObject().(*corev1.PersistentVolume)
		pv.UID = "replacement"
		pv.ResourceVersion = "2"
		err := client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("persistentvolumes"), pv, "")
		return true, pv, err
	})
	if err := s.syncPublication(ctx, client, v.ID); err == nil {
		t.Fatal("accepted deleted PV identity")
	}
	if _, err := client.CoreV1().PersistentVolumes().Get(ctx, v.ID, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("retry created an unmanaged bindable PV: %v", err)
	}
}
