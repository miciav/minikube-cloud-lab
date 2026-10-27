package cloud

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// ReconcileStorage provisions cloud volumes requested by cluster claims.
func (s *Service) ReconcileStorage(ctx context.Context, client kubernetes.Interface, cluster, server string) error {
	if net.ParseIP(server) == nil {
		return fmt.Errorf("%w: invalid storage server", ErrInvalid)
	}
	if err := ensureStorageClasses(ctx, client); err != nil {
		return err
	}
	claims, err := client.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	provisionErr := s.provisionClaims(ctx, client, cluster, server, claims.Items)
	return errors.Join(provisionErr, s.releaseClaims(ctx, client, cluster, server, claims.Items))
}

func ensureStorageClasses(ctx context.Context, client kubernetes.Interface) error {
	for _, item := range []struct {
		name   string
		policy corev1.PersistentVolumeReclaimPolicy
	}{{"cloud-standard", corev1.PersistentVolumeReclaimDelete}, {"cloud-retain", corev1.PersistentVolumeReclaimRetain}} {
		annotations := map[string]string{}
		if item.name == "cloud-standard" {
			annotations["storageclass.kubernetes.io/is-default-class"] = "true"
		}
		sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: item.name, Annotations: annotations}, Provisioner: "cloud.minikube.local/nfs", ReclaimPolicy: &item.policy}
		_, err := client.StorageV1().StorageClasses().Create(ctx, sc, metav1.CreateOptions{})
		if err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return err
			}
			current, err := client.StorageV1().StorageClasses().Get(ctx, item.name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if current.Provisioner != sc.Provisioner || current.ReclaimPolicy == nil || *current.ReclaimPolicy != item.policy {
				return fmt.Errorf("%w: storage class %s is not owned by cloud", ErrConflict, item.name)
			}
		}
	}
	return nil
}

func (s *Service) provisionClaims(ctx context.Context, client kubernetes.Interface, cluster, server string, claims []corev1.PersistentVolumeClaim) error {
	var result error
	for i := range claims {
		result = errors.Join(result, s.provisionClaim(ctx, client, cluster, server, &claims[i]))
	}
	return result
}

func (s *Service) provisionClaim(ctx context.Context, client kubernetes.Interface, cluster, server string, claim *corev1.PersistentVolumeClaim) error {
	if claim.Spec.StorageClassName == nil || claim.DeletionTimestamp != nil {
		return nil
	}
	policy, ok := map[string]string{"cloud-standard": "Delete", "cloud-retain": "Retain"}[*claim.Spec.StorageClassName]
	if !ok {
		return nil
	}
	size, ok := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	if !ok || len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		return nil
	}
	id := "pvc-" + string(claim.UID)
	var volume Volume
	if claim.Spec.VolumeName == "" {
		var err error
		volume, err = s.CreateVolume(id, cluster, claim.Namespace+"/"+claim.Name, size.String(), policy)
		if err != nil {
			return err
		}
	} else {
		for _, candidate := range s.Volumes() {
			if candidate.ID == claim.Spec.VolumeName {
				volume = candidate
				break
			}
		}
		if volume.ID == "" {
			return nil
		}
		capacity, err := resource.ParseQuantity(volume.Capacity)
		if err != nil || capacity.Cmp(size) < 0 || volume.Policy != policy {
			return fmt.Errorf("%w: incompatible retained volume", ErrInvalid)
		}
		if volume.Status == "Available" {
			pending, err := removeReleasedPV(ctx, client, server, volume)
			if err != nil {
				return err
			}
			if pending {
				return nil
			}
		}
		if err := s.AttachVolume(volume.ID, cluster, claim.Namespace+"/"+claim.Name, string(claim.UID)); err != nil {
			return err
		}
		size = capacity
	}
	reclaim := corev1.PersistentVolumeReclaimPolicy(volume.Policy)
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: volume.ID}, Spec: corev1.PersistentVolumeSpec{
		Capacity: corev1.ResourceList{corev1.ResourceStorage: size}, AccessModes: claim.Spec.AccessModes, PersistentVolumeReclaimPolicy: reclaim,
		StorageClassName: *claim.Spec.StorageClassName, ClaimRef: &corev1.ObjectReference{Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID},
		PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: server, Path: "/data/volumes/" + volume.ID}},
		MountOptions:           []string{"nfsvers=4.1"},
	}}
	return ensurePersistentVolume(ctx, client, pv)
}

func ensurePersistentVolume(ctx context.Context, client kubernetes.Interface, pv *corev1.PersistentVolume) error {
	if _, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing, err := client.CoreV1().PersistentVolumes().Get(ctx, pv.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if existing.Spec.NFS == nil || existing.Spec.NFS.Server != pv.Spec.NFS.Server || existing.Spec.NFS.Path != pv.Spec.NFS.Path || existing.Spec.ClaimRef == nil {
			return fmt.Errorf("%w: persistent volume %s is not owned by cloud", ErrConflict, pv.Name)
		}
		if existing.Spec.ClaimRef.UID != pv.Spec.ClaimRef.UID {
			return fmt.Errorf("%w: volume is still bound", ErrConflict)
		}
	}
	return nil
}

func removeReleasedPV(ctx context.Context, client kubernetes.Interface, server string, volume Volume) (bool, error) {
	pv, err := client.CoreV1().PersistentVolumes().Get(ctx, volume.ID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if pv.Spec.NFS == nil || pv.Spec.NFS.Server != server || pv.Spec.NFS.Path != "/data/volumes/"+volume.ID || pv.Spec.ClaimRef == nil || string(pv.Spec.ClaimRef.UID) != volume.ClaimUID {
		return false, fmt.Errorf("%w: persistent volume is not owned by cloud", ErrConflict)
	}
	if (pv.Status.Phase != corev1.VolumeReleased && pv.Status.Phase != corev1.VolumeFailed) || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		return false, ErrFailedPrecondition
	}
	options := metav1.DeleteOptions{}
	if pv.UID != "" && pv.ResourceVersion != "" {
		options.Preconditions = &metav1.Preconditions{UID: &pv.UID, ResourceVersion: &pv.ResourceVersion}
	}
	if err := client.CoreV1().PersistentVolumes().Delete(ctx, volume.ID, options); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	return true, nil
}

func (s *Service) releaseClaims(ctx context.Context, client kubernetes.Interface, cluster, server string, claims []corev1.PersistentVolumeClaim) error {
	var result error
	for _, volume := range s.Volumes() {
		result = errors.Join(result, s.releaseClaim(ctx, client, cluster, server, claims, volume))
	}
	return result
}

func (s *Service) releaseClaim(ctx context.Context, client kubernetes.Interface, cluster, server string, claims []corev1.PersistentVolumeClaim, volume Volume) error {
	if volume.Cluster != cluster || volume.Status != "Bound" {
		return nil
	}
	found := false
	for _, claim := range claims {
		if claim.Namespace+"/"+claim.Name == volume.Claim && string(claim.UID) == volume.ClaimUID {
			found = true
			break
		}
	}
	if found {
		return nil
	}
	policy, ready, err := releasedVolumePolicy(ctx, client, server, volume)
	if err != nil || !ready {
		return err
	}
	if err := s.releaseVolumeFor(volume.ID, volume.Cluster, volume.Claim, volume.ClaimUID, policy); err != nil {
		return err
	}
	return nil
}

func releasedVolumePolicy(ctx context.Context, client kubernetes.Interface, server string, volume Volume) (string, bool, error) {
	pv, err := client.CoreV1().PersistentVolumes().Get(ctx, volume.ID, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return "", false, err
	}
	policy := "Retain" // A missing PV cannot prove that deletion is safe.
	if err == nil {
		policy = string(pv.Spec.PersistentVolumeReclaimPolicy)
		if policy == "" {
			policy = "Retain"
		}
		if policy != "Delete" && policy != "Retain" {
			return "", false, fmt.Errorf("%w: unsupported reclaim policy", ErrInvalid)
		}
		if pv.Status.Phase != corev1.VolumeReleased && pv.Status.Phase != corev1.VolumeFailed {
			return "", false, nil
		}
		if pv.Spec.NFS == nil || pv.Spec.NFS.Server != server || pv.Spec.NFS.Path != "/data/volumes/"+volume.ID || pv.Spec.ClaimRef == nil || string(pv.Spec.ClaimRef.UID) != volume.ClaimUID {
			return "", false, fmt.Errorf("%w: persistent volume %s is not owned by cloud", ErrConflict, volume.ID)
		}
		if policy == "Retain" {
			return policy, true, nil
		}
		options := metav1.DeleteOptions{}
		if pv.UID != "" && pv.ResourceVersion != "" {
			options.Preconditions = &metav1.Preconditions{UID: &pv.UID, ResourceVersion: &pv.ResourceVersion}
		}
		if err := client.CoreV1().PersistentVolumes().Delete(ctx, volume.ID, options); err != nil {
			if !apierrors.IsNotFound(err) {
				return "", false, err
			}
			policy = "Retain"
		}
	}
	return policy, true, nil
}

// RunStorage reconciles claims for running clusters until shutdown.
func (s *Service) RunStorage(ctx context.Context, server string) error {
	if net.ParseIP(server) == nil {
		return fmt.Errorf("%w: invalid storage server", ErrInvalid)
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		s.retryVolumeDeletions()
		for _, cluster := range s.Clusters() {
			if cluster.Status != "RUNNING" {
				continue
			}
			s.workMu.Lock()
			current, err := s.Cluster(cluster.Name)
			if err == nil && current.Status == "RUNNING" {
				cfg, cfgErr := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: filepath.Join(s.dir, "minikube", "kubeconfig")}, &clientcmd.ConfigOverrides{CurrentContext: cluster.Name}).ClientConfig()
				if cfgErr == nil {
					cfg.Timeout = 5 * time.Second
					var client *kubernetes.Clientset
					client, cfgErr = kubernetes.NewForConfig(cfg)
					if cfgErr == nil {
						cfgErr = s.ReconcileStorage(ctx, client, cluster.Name, server)
					}
				}
				if cfgErr != nil && ctx.Err() == nil {
					s.logger.Error("storage reconciliation failed", "cluster", cluster.Name, "error", cfgErr)
				}
			}
			s.workMu.Unlock()
		}
	}
}
