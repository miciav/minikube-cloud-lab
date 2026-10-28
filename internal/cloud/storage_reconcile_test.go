package cloud_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestStorageReconcilerProvisionsPVCOnce(t *testing.T) {
	s, _, _ := newService(t)
	name := "cloud-standard"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default", UID: types.UID("12345678-aaaa-bbbb-cccc-123456789abc")}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	client := kfake.NewSimpleClientset(claim)
	for i := 0; i < 2; i++ {
		if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err != nil {
			t.Fatal(err)
		}
	}
	volumes := s.Volumes()
	if len(volumes) != 1 || volumes[0].Claim != "default/data" {
		t.Fatalf("cloud volumes: %+v", volumes)
	}
	pv, err := client.CoreV1().PersistentVolumes().Get(context.Background(), volumes[0].ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Spec.NFS == nil || pv.Spec.NFS.Server != "172.30.0.253" || pv.Spec.NFS.Path != "/data/volumes/"+volumes[0].ID || pv.Spec.ClaimRef.UID != claim.UID {
		t.Fatalf("bad volume: %+v", pv.Spec)
	}
}

func TestStorageReconcilerRejectsConflictingPV(t *testing.T) {
	s, _, _ := newService(t)
	name := "cloud-standard"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default", UID: types.UID("11111111-aaaa-bbbb-cccc-123456789abc")}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	conflict := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pvc-11111111-aaaa-bbbb-cccc-123456789abc"}}
	client := kfake.NewSimpleClientset(claim, conflict)
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err == nil {
		t.Fatal("accepted conflicting PV")
	}
}

func TestStorageReconcilerReleasesDeletedClaim(t *testing.T) {
	s, _, dir := newService(t)
	volume, err := s.CreateVolume("pvc-123", "demo", "default/data", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: volume.ID}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{Namespace: "default", Name: "data", UID: types.UID("123")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + volume.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
	client := kfake.NewSimpleClientset(pv)
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	got := s.Volumes()
	if len(got) != 1 || got[0].Status != "Available" {
		t.Fatalf("volume not retained: %+v", got)
	}
	if _, err := client.CoreV1().PersistentVolumes().Get(context.Background(), volume.ID, metav1.GetOptions{}); err != nil {
		t.Fatalf("Retain must keep released PV: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "storage", "volumes", volume.ID)); err != nil {
		t.Fatalf("retained data removed: %v", err)
	}
}

func TestStorageReconcilerInstallsCloudStorageClasses(t *testing.T) {
	s, _, _ := newService(t)
	client := kfake.NewSimpleClientset()
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cloud-standard", "cloud-retain"} {
		sc, err := client.StorageV1().StorageClasses().Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if sc.Provisioner != "cloud.minikube.local/nfs" {
			t.Fatalf("unexpected provisioner: %s", sc.Provisioner)
		}
	}
}

func TestStorageWorkerCancellationAndValidation(t *testing.T) {
	s, _, _ := newService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.RunStorage(ctx, "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if err := s.RunStorage(context.Background(), "not-an-ip"); err == nil {
		t.Fatal("accepted invalid storage address")
	}
}

func TestRetainedVolumeReattachedByNewPVC(t *testing.T) {
	s, _, _ := newService(t)
	v, err := s.CreateVolume("pvc-old", "old", "default/data", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "old", "default/data", "old"); err != nil {
		t.Fatal(err)
	}
	name := "cloud-retain"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "restored", Namespace: "default", UID: types.UID("new")}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, VolumeName: v.ID, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	client := kfake.NewSimpleClientset(claim)
	for i := 0; i < 2; i++ {
		if err := s.ReconcileStorage(context.Background(), client, "new", "172.30.0.253"); err != nil {
			t.Fatal(err)
		}
	}
	pv, err := client.CoreV1().PersistentVolumes().Get(context.Background(), v.ID, metav1.GetOptions{})
	if err != nil || pv.Spec.ClaimRef.UID != claim.UID {
		t.Fatalf("restored PV: %+v %v", pv, err)
	}
	got := s.Volumes()
	if len(got) != 1 || got[0].Status != "Bound" || got[0].Cluster != "new" || got[0].Claim != "default/restored" {
		t.Fatalf("attachment: %+v", got)
	}
}

func TestStorageReconcilerDeletesReleasedNFSVolumeWhenKubernetesReportsFailure(t *testing.T) {
	s, _, dir := newService(t)
	v, err := s.CreateVolume("pvc-123", "demo", "default/data", "1Gi", "Delete")
	if err != nil {
		t.Fatal(err)
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete, ClaimRef: &corev1.ObjectReference{Namespace: "default", Name: "data", UID: types.UID("123")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + v.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeFailed}}
	client := kfake.NewSimpleClientset(pv)
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if len(s.Volumes()) != 0 {
		t.Fatal("Delete volume remains")
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", "volumes", v.ID)); !os.IsNotExist(err) {
		t.Fatalf("data remains: %v", err)
	}
}

func TestBadClaimDoesNotBlockOtherClaimsOrReclamation(t *testing.T) {
	s, _, _ := newService(t)
	old, err := s.CreateVolume("pvc-old", "demo", "default/deleted", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	name := "cloud-standard"
	newClaim := func(id string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: "default", UID: types.UID(id)}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	}
	conflict := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pvc-bad"}}
	oldPV := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: old.ID}, Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{UID: types.UID("old")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + old.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
	client := kfake.NewSimpleClientset(newClaim("bad"), newClaim("good"), conflict, oldPV)
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err == nil {
		t.Fatal("conflict was not reported")
	}
	if _, err := client.CoreV1().PersistentVolumes().Get(context.Background(), "pvc-good", metav1.GetOptions{}); err != nil {
		t.Fatalf("good PVC was blocked: %v", err)
	}
	for _, v := range s.Volumes() {
		if v.ID == old.ID && v.Status != "Available" {
			t.Fatalf("reclamation blocked: %+v", v)
		}
	}
}

func TestStorageReconcilerHonoursChangedRetainPolicy(t *testing.T) {
	s, _, dir := newService(t)
	v, err := s.CreateVolume("pvc-123", "demo", "default/data", "1Gi", "Delete")
	if err != nil {
		t.Fatal(err)
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{UID: types.UID("123")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + v.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
	if err := s.ReconcileStorage(context.Background(), kfake.NewSimpleClientset(pv), "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	got := s.Volumes()
	if len(got) != 1 || got[0].Policy != "Retain" || got[0].Status != "Available" {
		t.Fatalf("PV policy ignored: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", "volumes", v.ID)); err != nil {
		t.Fatalf("retained data lost: %v", err)
	}
}

func TestStorageReconcilerRetainsDataIfPVDisappearsDuringDeletion(t *testing.T) {
	s, _, dir := newService(t)
	v, err := s.CreateVolume("pvc-123", "demo", "default/data", "1Gi", "Delete")
	if err != nil {
		t.Fatal(err)
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete, ClaimRef: &corev1.ObjectReference{UID: types.UID("123")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + v.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeFailed}}
	client := kfake.NewSimpleClientset(pv)
	client.PrependReactor("delete", "persistentvolumes", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "persistentvolumes"}, v.ID)
	})
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes(); len(got) != 1 || got[0].Policy != "Retain" {
		t.Fatalf("data not retained: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", "volumes", v.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestStorageWorkerReconcilesRunningClusterThroughInternalKubeconfig(t *testing.T) {
	s, _, dir := newService(t)
	create(t, s, 1)
	observed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/apis/storage.k8s.io/v1/storageclasses":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"apiVersion":"storage.k8s.io/v1","kind":"StorageClass","metadata":{"name":"cloud"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/persistentvolumeclaims":
			select {
			case observed <- struct{}{}:
			default:
			}
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"PersistentVolumeClaimList","items":[]}`)
		default:
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
		}
	}))
	defer server.Close()
	kubeDir := filepath.Join(dir, "minikube")
	if err := os.MkdirAll(kubeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &clientapi.Config{Clusters: map[string]*clientapi.Cluster{"demo": {Server: server.URL}}, AuthInfos: map[string]*clientapi.AuthInfo{"demo": {}}, Contexts: map[string]*clientapi.Context{"demo": {Cluster: "demo", AuthInfo: "demo"}}, CurrentContext: "demo"}
	if err := clientcmd.WriteToFile(*cfg, filepath.Join(kubeDir, "kubeconfig")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.RunStorage(ctx, "172.30.0.253") }()
	select {
	case <-observed:
		cancel()
	case <-ctx.Done():
		t.Fatal("worker never reconciled the cluster")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestStorageReconcilerRetriesFailedPVCreation(t *testing.T) {
	s, _, _ := newService(t)
	name := "cloud-standard"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default", UID: types.UID("retry")}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	client := kfake.NewSimpleClientset(claim)
	fail := true
	client.PrependReactor("create", "persistentvolumes", func(_ ktesting.Action) (bool, runtime.Object, error) {
		if fail {
			fail = false
			return true, nil, errors.New("temporary API failure")
		}
		return false, nil, nil
	})
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err == nil {
		t.Fatal("API failure hidden")
	}
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().PersistentVolumes().Get(context.Background(), "pvc-retry", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(s.Volumes()) != 1 {
		t.Fatalf("duplicate cloud volumes: %+v", s.Volumes())
	}
}

func TestStorageReconcilerRejectsForeignStorageClassAndListFailure(t *testing.T) {
	s, _, _ := newService(t)
	foreign := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "cloud-standard"}, Provisioner: "other.example/driver"}
	if err := s.ReconcileStorage(context.Background(), kfake.NewSimpleClientset(foreign), "demo", "172.30.0.253"); err == nil {
		t.Fatal("accepted foreign storage class")
	}
	client := kfake.NewSimpleClientset()
	client.PrependReactor("list", "persistentvolumeclaims", func(_ ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("API unavailable") })
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err == nil {
		t.Fatal("claim list failure hidden")
	}
}

func TestRetainPolicyAfterFailedDeletionPreservesVolume(t *testing.T) {
	s, _, _ := newService(t)
	v, err := s.CreateVolume("pvc-123", "demo", "default/data", "1Gi", "Delete")
	if err != nil {
		t.Fatal(err)
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{UID: types.UID("123")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + v.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeFailed}}
	if err := s.ReconcileStorage(context.Background(), kfake.NewSimpleClientset(pv), "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes(); len(got) != 1 || got[0].Status != "Available" || got[0].Policy != "Retain" {
		t.Fatalf("volume not available for recovery: %+v", got)
	}
}

func TestRetainedVolumeCanBeReattachedInSameCluster(t *testing.T) {
	s, _, _ := newService(t)
	v, err := s.CreateVolume("pvc-old", "demo", "default/old", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	name := "cloud-retain"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "default", UID: types.UID("new")}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, VolumeName: v.ID, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	old := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{Namespace: "default", Name: "old", UID: types.UID("old")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + v.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
	client := kfake.NewSimpleClientset(claim, old)
	for i := 0; i < 3; i++ {
		_ = s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253")
	}
	pv, err := client.CoreV1().PersistentVolumes().Get(context.Background(), v.ID, metav1.GetOptions{})
	if err != nil || pv.Spec.ClaimRef.UID != claim.UID {
		t.Fatalf("PV not reattached: %+v %v", pv, err)
	}
}

func TestMissingPVPreservesCloudData(t *testing.T) {
	s, _, dir := newService(t)
	v, err := s.CreateVolume("pvc-missing", "demo", "default/data", "1Gi", "Delete")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileStorage(context.Background(), kfake.NewSimpleClientset(), "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes(); len(got) != 1 || got[0].Policy != "Retain" || got[0].Status != "Available" {
		t.Fatalf("missing PV lost data: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", "volumes", v.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledRecoveryAfterPVDeleteFailureLeavesVolumeAvailable(t *testing.T) {
	s, _, _ := newService(t)
	v, err := s.CreateVolume("pvc-old", "demo", "default/old", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "demo", "default/old", "old"); err != nil {
		t.Fatal(err)
	}
	name := "cloud-retain"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "default", UID: types.UID("new")}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, VolumeName: v.ID, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	old := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{UID: types.UID("old")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + v.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
	client := kfake.NewSimpleClientset(claim, old)
	client.PrependReactor("delete", "persistentvolumes", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("temporary failure")
	})
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err == nil {
		t.Fatal("delete failure hidden")
	}
	if err := client.CoreV1().PersistentVolumeClaims("default").Delete(context.Background(), "new", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := s.Volumes(); len(got) != 1 || got[0].Status != "Available" || got[0].ClaimUID != "old" {
		t.Fatalf("volume stuck after cancelled recovery: %+v", got)
	}
}

func TestRecoveryHandlesFailedRetainedPV(t *testing.T) {
	s, _, _ := newService(t)
	v, err := s.CreateVolume("pvc-old", "demo", "default/old", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "demo", "default/old", "old"); err != nil {
		t.Fatal(err)
	}
	name := "cloud-retain"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "default", UID: types.UID("new")}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, VolumeName: v.ID, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	old := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{UID: types.UID("old")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + v.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeFailed}}
	client := kfake.NewSimpleClientset(claim, old)
	for i := 0; i < 3; i++ {
		if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err != nil {
			t.Fatal(err)
		}
	}
	pv, err := client.CoreV1().PersistentVolumes().Get(context.Background(), v.ID, metav1.GetOptions{})
	if err != nil || pv.Spec.ClaimRef.UID != claim.UID {
		t.Fatalf("not recovered: %+v %v", pv, err)
	}
}

func TestStorageControllerGuardsPVDeletionAgainstConcurrentChanges(t *testing.T) {
	s, _, _ := newService(t)
	v, err := s.CreateVolume("pvc-123", "demo", "default/data", "1Gi", "Delete")
	if err != nil {
		t.Fatal(err)
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID, UID: types.UID("pv-uid"), ResourceVersion: "42"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete, ClaimRef: &corev1.ObjectReference{UID: types.UID("123")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + v.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeFailed}}
	client := kfake.NewSimpleClientset(pv)
	client.PrependReactor("delete", "persistentvolumes", func(action ktesting.Action) (bool, runtime.Object, error) {
		opts := action.(ktesting.DeleteAction).GetDeleteOptions()
		if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != pv.UID || opts.Preconditions.ResourceVersion == nil || *opts.Preconditions.ResourceVersion != "42" {
			t.Fatalf("missing preconditions: %+v", opts)
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "persistentvolumes"}, v.ID, errors.New("PV changed"))
	})
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err == nil {
		t.Fatal("conflict hidden")
	}
	if got := s.Volumes(); len(got) != 1 || got[0].Status != "Bound" {
		t.Fatalf("volume changed after conflict: %+v", got)
	}
}

func TestRecoveryRefusesForeignReleasedPV(t *testing.T) {
	s, _, _ := newService(t)
	v, err := s.CreateVolume("pvc-old", "demo", "default/old", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "demo", "default/old", "old"); err != nil {
		t.Fatal(err)
	}
	name := "cloud-retain"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "default", UID: types.UID("new")}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, VolumeName: v.ID, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	foreign := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{UID: types.UID("other")}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "172.30.0.253", Path: "/data/volumes/" + v.ID}}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
	client := kfake.NewSimpleClientset(claim, foreign)
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err == nil {
		t.Fatal("accepted foreign PV")
	}
	if _, err := client.CoreV1().PersistentVolumes().Get(context.Background(), v.ID, metav1.GetOptions{}); err != nil {
		t.Fatalf("foreign PV deleted: %v", err)
	}
	if got := s.Volumes(); got[0].Status != "Available" {
		t.Fatalf("volume wrongly attached: %+v", got)
	}
}

func TestStorageBudgetLeavesClaimPendingWithOneWarning(t *testing.T) {
	s, _, _ := newService(t)
	if err := s.ConfigureStorage(&memoryStorage{}, "16Mi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	v, err := s.CreateAvailableVolume("16Mi")
	if err != nil {
		t.Fatal(err)
	}
	name := "cloud-standard"
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default", UID: "budget"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &name, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Mi")}}}}
	client := kfake.NewSimpleClientset(claim)
	for i := 0; i < 2; i++ {
		if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err == nil {
			t.Fatal("budget ignored")
		}
	}
	events, err := client.CoreV1().Events("default").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(events.Items) != 1 || events.Items[0].Reason != "StorageCapacityExceeded" {
		t.Fatalf("warnings %+v %v", events, err)
	}
	pvs, _ := client.CoreV1().PersistentVolumes().List(context.Background(), metav1.ListOptions{})
	if len(pvs.Items) != 0 {
		t.Fatal("PV allocated over budget")
	}
	if err := s.DeleteVolume(v.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileStorage(context.Background(), client, "demo", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	pv, err := client.CoreV1().PersistentVolumes().Get(context.Background(), "pvc-budget", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Spec.Capacity.Storage().Cmp(resource.MustParse("16Mi")) != 0 {
		t.Fatalf("effective capacity not advertised: %+v", pv.Spec.Capacity)
	}
}
