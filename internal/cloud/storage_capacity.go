package cloud

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/miciav/minikube-cloud-lab/internal/storage"
)

const storageOperationTimeout = 30 * time.Second

// ErrStorageCapacity means the cloud's allocation budget is exhausted.
var ErrStorageCapacity = errors.New("storage capacity exhausted")

// StorageCapacity describes allocated virtual disk capacity, including retained volumes.
type StorageCapacity struct {
	Total     string `json:"total"`
	Allocated string `json:"allocated"`
	Available string `json:"available"`
}

// ConfigureStorage enables bounded filesystems. Existing legacy data remains inspectable.
func (s *Service) ConfigureStorage(backend storage.Backend, total, server string) error {
	q, err := parseStorageBytes(total)
	if err != nil || backend == nil || net.ParseIP(server) == nil {
		return fmt.Errorf("%w: invalid storage capacity or backend", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storage = backend
	s.storageTotal = q
	s.storageServer = server
	return nil
}

func parseStorageBytes(q string) (int64, error) {
	size, err := resource.ParseQuantity(q)
	if err != nil || size.Sign() <= 0 {
		return 0, ErrInvalid
	}
	n, ok := size.AsInt64()
	if !ok || n > 1<<40 {
		return 0, ErrInvalid
	}
	return n, nil
}

func storageQuantity(n int64) string {
	q := resource.NewQuantity(n, resource.BinarySI)
	return q.String()
}

func (s *Service) storageAllocated() (int64, error) {
	var used int64
	for _, v := range s.state.Volumes {
		n, err := parseStorageBytes(v.Capacity)
		if err != nil || used > 1<<40-n {
			return 0, fmt.Errorf("%w: invalid stored storage capacity", ErrFailedPrecondition)
		}
		used += n
	}
	return used, nil
}

// StorageCapacity reports the fixed budget and outstanding reservations.
func (s *Service) StorageCapacity() (StorageCapacity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storage == nil {
		return StorageCapacity{}, fmt.Errorf("%w: bounded storage is not configured", ErrFailedPrecondition)
	}
	used, err := s.storageAllocated()
	if err != nil {
		return StorageCapacity{}, err
	}
	available := s.storageTotal - used
	if available < 0 {
		available = 0
	}
	return StorageCapacity{Total: storageQuantity(s.storageTotal), Allocated: storageQuantity(used), Available: storageQuantity(available)}, nil
}

// CreateAvailableVolume allocates an unclaimed Retain volume for an administrator.
func (s *Service) CreateAvailableVolume(capacity string) (Volume, error) {
	return s.allocateVolume("pvc-"+uuid.NewString(), "", "", capacity, "Retain")
}

func (s *Service) allocateVolume(id, cluster, claim, capacity, policy string) (Volume, error) {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	n, err := parseStorageBytes(capacity)
	if err != nil {
		return Volume{}, ErrInvalid
	}
	if n < 16<<20 {
		n = 16 << 20
	}
	n = (n + (1 << 20) - 1) / (1 << 20) * (1 << 20)
	if !validVolumeRequest(id, "validation", "validation", storageQuantity(n), policy) {
		return Volume{}, ErrInvalid
	}
	if (cluster == "") != (claim == "") {
		return Volume{}, ErrInvalid
	}
	s.mu.Lock()
	backend := s.storage
	if backend == nil {
		s.mu.Unlock()
		return Volume{}, fmt.Errorf("%w: bounded storage is not configured", ErrFailedPrecondition)
	}
	volume := Volume{ID: id, Cluster: cluster, Claim: claim, Capacity: storageQuantity(n), Policy: policy, Status: "Creating", Enforced: true}
	if claim != "" {
		volume.ClaimUID = id[4:]
	}
	volume, err = s.reserveVolume(volume, n)
	if err != nil {
		s.mu.Unlock()
		return Volume{}, err
	}

	s.mu.Unlock()
	if volume.Status != "Creating" {
		return volume, nil
	}
	// A durable Creating reservation survives cancellation or a failed backend call.
	ctx, cancel := context.WithTimeout(context.Background(), storageOperationTimeout)
	defer cancel()
	if err := backend.Ensure(ctx, id, n); err != nil {
		return Volume{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, v := range s.state.Volumes {
		if v.ID == id {
			if v.Status == "Deleting" {
				return Volume{}, ErrFailedPrecondition
			}
			if v.Status != "Creating" {
				return v, nil
			}
			next := s.state.clone()
			if claim == "" {
				next.Volumes[i].Status = "Available"
			} else {
				next.Volumes[i].Status = "Bound"
			}
			if err := s.store(next); err != nil {
				return Volume{}, err
			}
			return next.Volumes[i], nil
		}
	}
	return Volume{}, ErrNotFound
}

func (s *Service) reserveVolume(volume Volume, n int64) (Volume, error) {
	index := -1
	for i, v := range s.state.Volumes {
		if !v.Enforced {
			return Volume{}, fmt.Errorf("%w: legacy volumes require an explicit offline migration", ErrFailedPrecondition)
		}
		if v.ID != volume.ID {
			continue
		}
		if v.Cluster != volume.Cluster || v.Claim != volume.Claim || v.Capacity != volume.Capacity || v.Policy != volume.Policy || v.ClaimUID != volume.ClaimUID || v.Status == "Deleting" {
			return Volume{}, ErrConflict
		}
		volume = v
		index = i
	}
	if index < 0 {
		used, err := s.storageAllocated()
		if err != nil {
			return Volume{}, err
		}
		if n > s.storageTotal-used {
			return Volume{}, ErrStorageCapacity
		}
		next := s.state.clone()
		next.Volumes = append(next.Volumes, volume)
		if err := s.store(next); err != nil {
			return Volume{}, err
		}
	}
	return volume, nil
}
