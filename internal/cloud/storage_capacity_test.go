package cloud_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

type memoryStorage struct {
	mu     sync.Mutex
	fail   bool
	images map[string]int64
}

func (b *memoryStorage) Ensure(_ context.Context, id string, n int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return errors.New("storage offline")
	}
	if b.images == nil {
		b.images = map[string]int64{}
	}
	b.images[id] = n
	return nil
}

func (b *memoryStorage) Delete(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return errors.New("storage offline")
	}
	delete(b.images, id)
	return nil
}

func TestStorageCapacityCountsRetainedAndReleasesOnlyAfterDelete(t *testing.T) {
	s, _, _ := newService(t)
	b := &memoryStorage{}
	if err := s.ConfigureStorage(b, "8Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAvailableVolume("3Gi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateAvailableVolume("5Gi"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateAvailableVolume("16Mi"); !errors.Is(err, cloud.ErrStorageCapacity) {
		t.Fatalf("over allocation: %v", err)
	}
	st, err := s.StorageCapacity()
	if err != nil || st.Allocated != "8Gi" || st.Available != "0" {
		t.Fatalf("status %+v %v", st, err)
	}
	b.fail = true
	if err = s.DeleteVolume(a.ID); err == nil {
		t.Fatal("failed storage deletion accepted")
	}
	st, _ = s.StorageCapacity()
	if st.Allocated != "8Gi" {
		t.Fatalf("freed budget before deletion: %+v", st)
	}
	b.fail = false
	if err = s.DeleteVolume(a.ID); err != nil {
		t.Fatal(err)
	}
	st, _ = s.StorageCapacity()
	if st.Available != "3Gi" {
		t.Fatalf("budget not released %+v", st)
	}
}

func TestStorageCapacityConcurrentAllocations(t *testing.T) {
	s, _, _ := newService(t)
	if err := s.ConfigureStorage(&memoryStorage{}, "8Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.CreateAvailableVolume("5Gi"); errs <- err }()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, cloud.ErrStorageCapacity) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful allocations=%d", successes)
	}
}

func TestStorageCapacityFailedProvisionRetainsReservationAndRetries(t *testing.T) {
	s, f, dir := newService(t)
	b := &memoryStorage{fail: true}
	if err := s.ConfigureStorage(b, "1Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVolume("pvc-retry", "demo", "default/data", "1Gi", "Retain"); err == nil {
		t.Fatal("offline storage accepted")
	}
	st, _ := s.StorageCapacity()
	if st.Allocated != "1Gi" {
		t.Fatalf("reservation lost %+v", st)
	}
	restarted, err := cloud.NewService(f, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.fail = false
	if err := restarted.ConfigureStorage(b, "1Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	v, err := restarted.CreateVolume("pvc-retry", "demo", "default/data", "1Gi", "Retain")
	if err != nil || v.Status != "Bound" {
		t.Fatalf("retry %+v %v", v, err)
	}
	if err = restarted.ReleaseVolumeFor(v.ID, "demo", "default/data", "retry"); err != nil {
		t.Fatal(err)
	}
	st, _ = restarted.StorageCapacity()
	if st.Allocated != "1Gi" {
		t.Fatalf("retained reservation lost %+v", st)
	}
	if err = restarted.ConfigureStorage(b, "16Mi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.CreateAvailableVolume("16Mi"); !errors.Is(err, cloud.ErrStorageCapacity) {
		t.Fatalf("lowered budget %v", err)
	}
	if err = restarted.DeleteVolume(v.ID); err != nil {
		t.Fatal(err)
	}
}

func TestStorageCapacityRejectsLegacyAndInvalidQuantities(t *testing.T) {
	s, _, _ := newService(t)
	if _, err := s.CreateVolume("pvc-legacy", "demo", "default/data", "1Gi", "Retain"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureStorage(&memoryStorage{}, "8Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAvailableVolume("1Gi"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("legacy data ignored: %v", err)
	}
	for _, q := range []string{"0", "-1Gi", "invalid", "9223372036854775808"} {
		if err := s.ConfigureStorage(&memoryStorage{}, q, "172.30.0.253"); err == nil {
			t.Fatalf("invalid budget accepted %s", q)
		}
	}
}

func TestStorageCapacityRejectsInvalidAndConflictingRequests(t *testing.T) {
	s, _, _ := newService(t)
	if err := s.ConfigureStorage(&memoryStorage{}, "1Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"0", "bad", "-1Gi", "2Ti"} {
		if _, err := s.CreateAvailableVolume(q); !errors.Is(err, cloud.ErrInvalid) {
			t.Fatalf("capacity %s: %v", q, err)
		}
	}
	if _, err := s.CreateVolume("../escape", "demo", "default/data", "64Mi", "Retain"); !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("invalid ID %v", err)
	}
	if _, err := s.CreateVolume("pvc-valid", "demo", "", "64Mi", "Retain"); !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("invalid claim %v", err)
	}
	if _, err := s.CreateVolume("pvc-valid", "demo", "default/data", "64Mi", "Retain"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVolume("pvc-valid", "demo", "default/data", "128Mi", "Retain"); !errors.Is(err, cloud.ErrConflict) {
		t.Fatalf("conflicting retry %v", err)
	}
	if _, err := s.CreateVolume("pvc-valid", "demo", "default/data", "64Mi", "Retain"); err != nil {
		t.Fatal(err)
	}
}

func TestStorageCapacityPersistsReservationBeforeAllocating(t *testing.T) {
	s, _, dir := newService(t)
	b := &memoryStorage{}
	if err := s.ConfigureStorage(b, "1Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAvailableVolume("64Mi"); err == nil {
		t.Fatal("allocation accepted failed reservation save")
	}
	status, err := s.StorageCapacity()
	if err != nil || status.Allocated != "0" || len(b.images) != 0 {
		t.Fatalf("non durable allocation %+v %v", status, err)
	}
}
