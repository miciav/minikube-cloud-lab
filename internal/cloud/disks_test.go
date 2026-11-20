package cloud_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

type diskBackend struct {
	*fake.Infra
	failure error
}

func (b *diskBackend) SetInstanceDisk(context.Context, string, string, bool) error { return b.failure }

func TestDiskReservationSurvivesAmbiguousGuestFailure(t *testing.T) {
	t.Setenv("CLOUD_DISK_VOLUME", "test")
	t.Setenv("CLOUD_ROOTLESS", "")
	dir := t.TempDir()
	backend := &diskBackend{Infra: fake.New()}
	open := func() *cloud.Service {
		s, err := cloud.NewService(backend, dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	svc := open()
	for _, name := range []string{"one", "two"} {
		createInstance(t, svc, name)
		if err := os.MkdirAll(filepath.Join(dir, "instance-disks", "slots", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	change := func(action, owner string) error {
		_, err := svc.ChangeDisk(t.Context(), action, "data", "64Mi", owner)
		return err
	}
	if err := change("create", ""); err != nil {
		t.Fatal(err)
	}
	backend.failure = errors.New("guest timeout")
	if err := change("attach", "one"); !errors.Is(err, backend.failure) {
		t.Fatal(err)
	}
	svc = open()
	if err := change("attach", "two"); !errors.Is(err, cloud.ErrConflict) {
		t.Fatal(err)
	}
	if err := change("delete", ""); !errors.Is(err, cloud.ErrConflict) {
		t.Fatal(err)
	}
	if err := change("detach", "one"); !errors.Is(err, backend.failure) {
		t.Fatal(err)
	}
	backend.failure = nil
	if err := change("detach", "one"); err != nil {
		t.Fatal(err)
	}
	if err := change("attach", "two"); err != nil {
		t.Fatal(err)
	}
	ds, err := svc.InstanceDisks()
	if err != nil || len(ds) != 1 || ds[0].Instance != "two" || ds[0].Bytes != 64<<20 {
		t.Fatalf("disks %v: %v", ds, err)
	}
}

func TestDiskValidation(t *testing.T) {
	s, _, _ := newService(t)
	t.Setenv("CLOUD_DISK_VOLUME", "test")
	t.Setenv("CLOUD_DISK_VOLUME", "")
	if _, err := s.InstanceDisks(); !errors.Is(err, cloud.ErrUnavailable) {
		t.Fatal(err)
	}
	t.Setenv("CLOUD_DISK_VOLUME", "test")
	for _, test := range []struct{ name, size string }{{"../x", "1Gi"}, {"data", "0"}, {"data", "1"}, {"data", "-1Gi"}, {"data", "oops"}} {
		if _, err := s.ChangeDisk(t.Context(), "create", test.name, test.size, ""); !errors.Is(err, cloud.ErrInvalid) {
			t.Errorf("%v: %v", test, err)
		}
	}
}

func TestDiskAttachmentPreconditionsAndDeletion(t *testing.T) {
	t.Setenv("CLOUD_DISK_VOLUME", "test")
	dir := t.TempDir()
	backend := &diskBackend{Infra: fake.New()}
	s, err := cloud.NewService(backend, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	change := func(action string) error {
		_, err := s.ChangeDisk(t.Context(), action, "disk", "1Gi", "vm")
		return err
	}
	require := func(action string, want error) {
		t.Helper()
		if err := change(action); !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", action, err, want)
		}
	}
	require("delete", cloud.ErrNotFound)
	require("create", nil)
	require("create", cloud.ErrConflict)
	require("bad", cloud.ErrInvalid)
	require("attach", cloud.ErrNotFound)
	createInstance(t, s, "vm")
	require("detach", nil)
	require("attach", cloud.ErrFailedPrecondition)
	slot := filepath.Join(dir, "instance-disks", "slots", "vm")
	if err := os.MkdirAll(slot, 0o700); err != nil {
		t.Fatal(err)
	}
	// A conflicting file in the guest slot must never be replaced or exposed.
	link := filepath.Join(slot, "disk")
	if err := os.WriteFile(link, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := change("attach"); err == nil {
		t.Fatal("replaced foreign backing file")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	require("attach", nil)
	backend.failure = infra.ErrDiskBusy
	require("detach", cloud.ErrConflict)
	backend.failure = nil
	stop, err := s.StopInstance("vm", "student")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, stop)
	require("detach", cloud.ErrConflict)
	del, err := s.DeleteInstance("vm", "student")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, del)
	require("delete", nil)
	require("delete", cloud.ErrNotFound)
	t.Setenv("CLOUD_DISK_VOLUME", "")
	require("create", cloud.ErrUnavailable)
}

func TestDiskInventoryAfterInterruptedCreation(t *testing.T) {
	t.Setenv("CLOUD_DISK_VOLUME", "test")
	s, _, dir := newService(t)
	images := filepath.Join(dir, "instance-disks", "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, ".create-partial"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if disks, err := s.InstanceDisks(); err != nil || len(disks) != 0 {
		t.Fatalf("partial image visible: %v %v", disks, err)
	}
}

func TestInvalidDiskStorageFailsClosed(t *testing.T) {
	t.Setenv("CLOUD_DISK_VOLUME", "test")
	s, _, dir := newService(t)
	root := filepath.Join(dir, "instance-disks")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	images := filepath.Join(root, "images")
	if err := os.WriteFile(images, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstanceDisks(); err == nil {
		t.Fatal("unreadable inventory appeared empty")
	}
	if _, err := s.ChangeDisk(t.Context(), "create", "test", "512", ""); err == nil {
		t.Fatal("invalid storage accepted")
	}
	if err := os.Remove(images); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(images, filepath.Join(images, "foreign")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstanceDisks(); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatal(err)
	}
	if _, err := s.ChangeDisk(t.Context(), "delete", "foreign", "", ""); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatal(err)
	}
}
