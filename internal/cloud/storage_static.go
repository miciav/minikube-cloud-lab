package cloud

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

func (s *Service) volume(id string) (Volume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.state.Volumes {
		if v.ID == id {
			return v, nil
		}
	}
	return Volume{}, ErrNotFound
}

func (s *Service) replaceVolume(old, next Volume) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, v := range s.state.Volumes {
		if v.ID == old.ID {
			if v != old {
				return ErrConflict
			}
			st := s.state.clone()
			st.Volumes[i] = next
			return s.store(st)
		}
	}
	return ErrNotFound
}

// PublishVolume reserves a volume exclusively and publishes an unbound static PV.
func (s *Service) PublishVolume(ctx context.Context, id, cluster string) error {
	return s.PublishVolumeIfUnchanged(ctx, id, cluster, nil)
}

// PublishVolumeIfUnchanged checks the selected cluster generation before reserving the volume.
func (s *Service) PublishVolumeIfUnchanged(ctx context.Context, id, cluster string, expected *time.Time) error {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if err := s.reservePublication(id, cluster, expected); err != nil {
		return err
	}
	client, err := s.clusterStorageClient(cluster)
	if err != nil {
		return err
	}
	return s.syncPublication(ctx, client, id)
}

func (s *Service) reservePublication(id, cluster string, expected *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.ready(cluster)
	if err != nil {
		return err
	}
	if expected != nil && !c.CreatedAt.Equal(*expected) {
		return fmt.Errorf("%w: cluster changed; refresh before publishing", ErrFailedPrecondition)
	}
	for i, v := range s.state.Volumes {
		if v.ID == id {
			if v.Static && v.Cluster == cluster && (v.Status == "Publishing" || v.Status == "Published" || v.Status == "Bound" || v.Status == "Released") {
				return nil
			}
			if !v.Enforced || v.Policy != "Retain" || v.Status != "Available" {
				return ErrFailedPrecondition
			}
			next := s.state.clone()
			v.Cluster = cluster
			v.Claim = ""
			v.ClaimUID = ""
			v.PVUID = ""
			v.Static = true
			v.Status = "Publishing"
			next.Volumes[i] = v
			return s.store(next)
		}
	}
	return ErrNotFound
}

func (s *Service) syncPublication(ctx context.Context, client kubernetes.Interface, id string) error {
	v, err := s.volume(id)
	if err != nil {
		return err
	}
	if !v.Static || v.Status == "Unpublishing" {
		return ErrFailedPrecondition
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: v.ID, Labels: map[string]string{"cloud.minikube.local/volume": v.ID}}, Spec: corev1.PersistentVolumeSpec{
		Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(v.Capacity)}, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, StorageClassName: "cloud-static", MountOptions: []string{"nfsvers=4.1"}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: s.storageServer, Path: "/data/volumes/" + v.ID}},
	}}
	var current *corev1.PersistentVolume
	if v.Status == "Publishing" && v.PVUID == "" {
		current, err = client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			current, err = client.CoreV1().PersistentVolumes().Get(ctx, id, metav1.GetOptions{})
		}
	} else {
		current, err = client.CoreV1().PersistentVolumes().Get(ctx, id, metav1.GetOptions{})
	}

	if err != nil {
		return err
	}
	if err = s.checkStaticPV(v, current); err != nil {
		return err
	}
	if v.Status != "Publishing" {
		return nil
	}
	next := v
	next.Status = "Published"
	next.PVUID = string(current.UID)
	return s.replaceVolume(v, next)
}

func (s *Service) checkStaticPV(v Volume, pv *corev1.PersistentVolume) error {
	if pv.Labels["cloud.minikube.local/volume"] != v.ID || pv.Spec.NFS == nil || pv.Spec.NFS.Server != s.storageServer || pv.Spec.NFS.Path != "/data/volumes/"+v.ID || pv.Spec.StorageClassName != "cloud-static" || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain || pv.Spec.Capacity.Storage().Cmp(resource.MustParse(v.Capacity)) != 0 || len(pv.Spec.AccessModes) != 1 || pv.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		return fmt.Errorf("%w: static persistent volume identity differs", ErrConflict)
	}
	if v.PVUID != "" && string(pv.UID) != v.PVUID {
		return fmt.Errorf("%w: persistent volume was replaced", ErrConflict)
	}
	return nil
}

// UnpublishVolume removes only an unused owned PV and preserves the cloud data.
func (s *Service) UnpublishVolume(ctx context.Context, id string) error {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	v, err := s.volume(id)
	if err != nil {
		return err
	}
	if v.Status == "Available" {
		return nil
	}
	s.mu.Lock()
	_, err = s.ready(v.Cluster)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	client, err := s.clusterStorageClient(v.Cluster)
	if err != nil {
		return err
	}
	return s.unpublishVolume(ctx, client, id)
}

func (s *Service) unpublishVolume(ctx context.Context, client kubernetes.Interface, id string) error {
	v, err := s.volume(id)
	if err != nil {
		return err
	}
	if !v.Static {
		return ErrFailedPrecondition
	}
	pv, err := client.CoreV1().PersistentVolumes().Get(ctx, id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if v.Status != "Unpublishing" {
			return fmt.Errorf("%w: missing PV does not prove safe detachment", ErrFailedPrecondition)
		}
		return s.finishUnpublish(v)
	}
	if err != nil {
		return err
	}
	if err = s.checkStaticPV(v, pv); err != nil {
		return err
	}
	if pv.Status.Phase == corev1.VolumeBound || (pv.Spec.ClaimRef != nil && pv.Status.Phase != corev1.VolumeReleased && pv.Status.Phase != corev1.VolumeFailed) {
		return fmt.Errorf("%w: persistent volume still has a claim", ErrFailedPrecondition)
	}
	if pv.UID == "" || pv.ResourceVersion == "" {
		return fmt.Errorf("%w: persistent volume lacks deletion preconditions", ErrFailedPrecondition)
	}
	if v.Status != "Unpublishing" {
		next := v
		next.Status = "Unpublishing"
		if err = s.replaceVolume(v, next); err != nil {
			return err
		}
		v = next
	}
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pv.UID, ResourceVersion: &pv.ResourceVersion}}
	if err = client.CoreV1().PersistentVolumes().Delete(ctx, id, options); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := client.CoreV1().PersistentVolumes().Get(ctx, id, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: waiting for persistent volume removal", ErrFailedPrecondition)
		}
		return err
	}

	return s.finishUnpublish(v)
}

func (s *Service) finishUnpublish(v Volume) error {
	next := v
	next.Status = "Available"
	next.Cluster = ""
	next.Claim = ""
	next.ClaimUID = ""
	next.PVUID = ""
	next.Static = false
	return s.replaceVolume(v, next)
}

func (s *Service) reconcileStatic(ctx context.Context, client kubernetes.Interface, cluster string, claims []corev1.PersistentVolumeClaim) error {
	var result error
	for _, v := range s.Volumes() {
		if v.Static && v.Cluster == cluster {
			result = errors.Join(result, s.reconcileStaticVolume(ctx, client, v, claims))
		}
	}
	return result
}

func (s *Service) reconcileStaticVolume(ctx context.Context, client kubernetes.Interface, v Volume, claims []corev1.PersistentVolumeClaim) error {
	if v.Status == "Publishing" {
		return s.syncPublication(ctx, client, v.ID)
	}
	if v.Status == "Unpublishing" {
		return s.unpublishVolume(ctx, client, v.ID)
	}
	pv, err := client.CoreV1().PersistentVolumes().Get(ctx, v.ID, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = s.checkStaticPV(v, pv); err != nil {
		return err
	}
	next := v
	switch pv.Status.Phase {
	case corev1.VolumeBound:
		if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID == "" {
			return ErrConflict
		}
		found := false
		for _, claim := range claims {
			if claim.UID == pv.Spec.ClaimRef.UID && claim.Namespace == pv.Spec.ClaimRef.Namespace && claim.Name == pv.Spec.ClaimRef.Name && claim.Spec.VolumeName == v.ID && claim.Spec.StorageClassName != nil && *claim.Spec.StorageClassName == "cloud-static" {
				found = true
				next.Status = "Bound"
				next.Claim = claim.Namespace + "/" + claim.Name
				next.ClaimUID = string(claim.UID)
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: static binding claim not observed", ErrFailedPrecondition)
		}
	case corev1.VolumeReleased, corev1.VolumeFailed:
		if pv.Spec.ClaimRef == nil || string(pv.Spec.ClaimRef.UID) != v.ClaimUID {
			return ErrConflict
		}
		next.Status = "Released"
	}
	if next == v {
		return nil
	}
	return s.replaceVolume(v, next)
}

func (s *Service) clusterStorageClient(cluster string) (kubernetes.Interface, error) {
	cfg, err := s.clusterRESTConfig(cluster)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 5 * time.Second
	return kubernetes.NewForConfig(cfg)
}

func (s *Service) clearClusterPublications(cluster string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state.clone()
	for i, v := range next.Volumes {
		if v.Static && v.Cluster == cluster {
			v.Static = false
			v.PVUID = ""
			v.Cluster = ""
			v.Claim = ""
			v.ClaimUID = ""
			v.Status = "Available"
			next.Volumes[i] = v
		}
	}
	return s.store(next)
}
