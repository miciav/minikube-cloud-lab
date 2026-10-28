package cloud_test

import (
	"os"
	"testing"
)

func TestStorageDoesNotAllocateWithoutDurableDirectoryEntry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires permission enforcement for a non-root process")
	}
	s, _, dir := newService(t)
	backend := &memoryStorage{}
	if err := s.ConfigureStorage(backend, "1Gi", "172.30.0.253"); err != nil {
		t.Fatal(err)
	}
	// Creating/renaming a file works, but opening the directory for fsync must fail.
	// #nosec G302 -- directory traversal/write permissions intentionally exercise fsync failure.
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- restore private directory permissions for test cleanup.
	defer func() { _ = os.Chmod(dir, 0o700) }()
	if _, err := s.CreateAvailableVolume("64Mi"); err == nil {
		t.Fatal("accepted allocation without confirming durable directory entry")
	}
	if len(backend.images) != 0 {
		t.Fatal("backing image created before durable reservation")
	}
}
