package cloud

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Volumes returns the cloud volume inventory.
func (s *Service) Volumes() []Volume {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Volume{}, s.state.Volumes...)
}

// CreateVolume allocates a volume for a Kubernetes claim.
func (s *Service) CreateVolume(id, cluster, claim, capacity, policy string) (Volume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validVolumeRequest(id, cluster, claim, capacity, policy) {
		return Volume{}, ErrInvalid
	}
	volume := Volume{ID: id, Cluster: cluster, Claim: claim, ClaimUID: strings.TrimPrefix(id, "pvc-"), Capacity: capacity, Policy: policy, Status: "Bound"}
	for _, v := range s.state.Volumes {
		if v.ID == id {
			if v != volume {
				return Volume{}, ErrConflict
			}
			return v, nil
		}
	}
	root, err := s.volumeRoot()
	if err != nil {
		return Volume{}, err
	}
	defer func() { _ = root.Close() }()
	// #nosec G301 -- student workloads can use arbitrary UIDs.
	err = root.Mkdir(id, 0o777)
	created := err == nil
	if err != nil && !errors.Is(err, os.ErrExist) {
		return Volume{}, err
	}
	rollback := func(err error) error {
		if created {
			return errors.Join(err, root.Remove(id))
		}
		return err
	}
	info, err := root.Lstat(id)
	if err != nil || !info.IsDir() {
		return Volume{}, rollback(fmt.Errorf("%w: invalid volume directory", ErrConflict))
	}
	f, err := root.Open(id)
	if err != nil {
		return Volume{}, rollback(err)
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return Volume{}, rollback(fmt.Errorf("%w: volume directory changed", ErrConflict))
	}
	_, readErr := f.Readdirnames(1)
	if !errors.Is(readErr, io.EOF) {
		_ = f.Close()
		return Volume{}, rollback(fmt.Errorf("%w: volume directory is not empty", ErrConflict))
	}
	// #nosec G302 -- student workloads can use arbitrary UIDs.
	chmodErr := f.Chmod(0o777)
	closeErr := f.Close()
	if err := errors.Join(chmodErr, closeErr); err != nil {
		return Volume{}, rollback(err)
	}
	next := s.state.clone()
	next.Volumes = append(next.Volumes, volume)
	if err := s.store(next); err != nil {
		return Volume{}, rollback(err)
	}
	return volume, nil
}

// ReleaseVolumeFor applies the reclaim policy to a detached claim.
func (s *Service) ReleaseVolumeFor(id, cluster, claim, uid string) error {
	return s.releaseVolumeFor(id, cluster, claim, uid, "")
}

func (s *Service) releaseVolumeFor(id, cluster, claim, uid, policy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, v := range s.state.Volumes {
		if v.ID != id {
			continue
		}
		if v.Status != "Bound" || v.Cluster != cluster || v.Claim != claim || v.ClaimUID != uid {
			return ErrFailedPrecondition
		}
		if policy == "" {
			policy = v.Policy
		}
		if policy != "Delete" && policy != "Retain" {
			return ErrInvalid
		}
		if policy == "Delete" {
			return s.deleteVolume(i)
		}
		next := s.state.clone()
		next.Volumes[i].Policy = policy
		next.Volumes[i].Status = "Available"
		return s.store(next)
	}
	return ErrNotFound
}

// AttachVolume reassigns an available retained volume.
func (s *Service) AttachVolume(id, cluster, claim, uid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cluster == "" || claim == "" || uid == "" {
		return ErrInvalid
	}
	for i, v := range s.state.Volumes {
		if v.ID != id {
			continue
		}
		if v.Status == "Bound" && v.Cluster == cluster && v.Claim == claim && v.ClaimUID == uid {
			return nil
		}
		if v.Status != "Available" {
			return ErrFailedPrecondition
		}
		next := s.state.clone()
		next.Volumes[i].Status = "Bound"
		next.Volumes[i].Cluster = cluster
		next.Volumes[i].Claim = claim
		next.Volumes[i].ClaimUID = uid
		return s.store(next)
	}
	return ErrNotFound
}

// DeleteVolume removes an available volume and its data.
func (s *Service) DeleteVolume(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, v := range s.state.Volumes {
		if v.ID != id {
			continue
		}
		if v.Status != "Available" && v.Status != "Deleting" {
			return ErrFailedPrecondition
		}
		return s.deleteVolume(i)
	}
	return ErrNotFound
}

func (s *Service) deleteVolume(i int) error {
	v := s.state.Volumes[i]
	if len(validation.IsDNS1123Label(v.ID)) != 0 || !strings.HasPrefix(v.ID, "pvc-") {
		return fmt.Errorf("%w: invalid stored volume", ErrInvalid)
	}
	if v.Status != "Deleting" {
		next := s.state.clone()
		next.Volumes[i].Status = "Deleting"
		if err := s.store(next); err != nil {
			return err
		}
	}
	root, err := s.volumeRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll(v.ID); err != nil {
		return err
	}
	next := s.state.clone()
	next.Volumes = append(next.Volumes[:i], next.Volumes[i+1:]...)
	return s.store(next)
}

// volumeRoot keeps deletion within the actual storage directory, even if it is renamed.
func (s *Service) volumeRoot() (*os.Root, error) {
	base := filepath.Join(s.dir, "storage", "volumes")
	// #nosec G301 -- NFS clients need to traverse the storage directory.
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	info, err := os.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: storage path is not a directory", ErrInvalid)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: storage directory changed", ErrConflict)
	}
	return root, nil
}

func (s *Service) retryVolumeDeletions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < len(s.state.Volumes); {
		if s.state.Volumes[i].Status != "Deleting" {
			i++
			continue
		}
		id := s.state.Volumes[i].ID
		if err := s.deleteVolume(i); err != nil {
			s.logger.Error("volume cleanup failed", "volume", id, "error", err)
			i++
		}
	}
}

func validVolumeRequest(id, cluster, claim, capacity, policy string) bool {
	size, err := resource.ParseQuantity(capacity)
	return strings.HasPrefix(id, "pvc-") && len(validation.IsDNS1123Label(id)) == 0 && cluster != "" && claim != "" && err == nil && size.Sign() > 0 && (policy == "Retain" || policy == "Delete")
}
