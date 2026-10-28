package cloud

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
	return errors.Join(provisionErr, s.releaseClaims(ctx, client, cluster, server, claims.Items), s.reconcileStatic(ctx, client, cluster, claims.Items))
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
	policy := corev1.PersistentVolumeReclaimRetain
	_, err := client.StorageV1().StorageClasses().Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "cloud-static"}, Provisioner: "kubernetes.io/no-provisioner", ReclaimPolicy: &policy}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		sc, e := client.StorageV1().StorageClasses().Get(ctx, "cloud-static", metav1.GetOptions{})
		if e != nil {
			return e
		}
		if sc.Provisioner != "kubernetes.io/no-provisioner" {
			return ErrConflict
		}
		return nil
	}
	return err
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
		volume, err = s.createClaimVolume(ctx, client, claim, id, cluster, size.String(), policy)
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
	size = resource.MustParse(volume.Capacity)
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
	if volume.Static || volume.Cluster != cluster || volume.Status != "Bound" {
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
	if err := s.releaseVolumeFor(volume.ID, volume.Cluster, volume.Claim, volume.ClaimUID, policy); err != nil { //nolint:contextcheck // durable deletion intent survives worker shutdown.
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
		s.retryVolumeDeletions() //nolint:contextcheck // pending deletion recovery has its own bounded context.
		for _, cluster := range s.Clusters() {
			if cluster.Status != "RUNNING" {
				continue
			}
			s.workMu.Lock()
			current, err := s.Cluster(cluster.Name)
			if err == nil && current.Status == "RUNNING" {
				client, cfgErr := s.clusterStorageClient(cluster.Name)
				if cfgErr == nil {
					cfgErr = s.ReconcileStorage(ctx, client, cluster.Name, server)
				}

				if cfgErr != nil && ctx.Err() == nil {
					s.logger.Error("storage reconciliation failed", "cluster", cluster.Name, "error", cfgErr)
				}
			}
			s.workMu.Unlock()
		}
	}
}

func storageCapacityEvent(ctx context.Context, client kubernetes.Interface, claim *corev1.PersistentVolumeClaim) error {
	now := metav1.Now()
	event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "storagecapacity-" + string(claim.UID), Namespace: claim.Namespace}, InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID, APIVersion: "v1"}, Type: corev1.EventTypeWarning, Reason: "StorageCapacityExceeded", Message: "The cloud storage allocation budget is exhausted; delete unused cloud volumes or increase CLOUD_STORAGE_CAPACITY.", Source: corev1.EventSource{Component: "cloud.minikube.local/nfs"}, FirstTimestamp: now, LastTimestamp: now, Count: 1}
	_, err := client.CoreV1().Events(claim.Namespace).Create(ctx, event, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (s *Service) createClaimVolume(ctx context.Context, client kubernetes.Interface, claim *corev1.PersistentVolumeClaim, id, cluster, capacity, policy string) (Volume, error) {
	volume, err := s.CreateVolume(id, cluster, claim.Namespace+"/"+claim.Name, capacity, policy) //nolint:contextcheck // durable allocation must survive reconciliation cancellation.
	if errors.Is(err, ErrStorageCapacity) {
		err = errors.Join(err, storageCapacityEvent(ctx, client, claim))
	}
	return volume, err
}
