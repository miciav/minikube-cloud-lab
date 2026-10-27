package cloud_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func TestRetainedVolumeSurvivesRestartAndCanBeReused(t *testing.T) {
	s, f, dir := newService(t)
	v, err := s.CreateVolume("pvc-123", "demo", "default/data", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "storage", "volumes", v.ID, "data")
	if err := os.WriteFile(path, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVolume(v.ID); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("delete in-use volume: %v", err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "demo", "default/data", strings.TrimPrefix(v.ID, "pvc-")); err != nil {
		t.Fatal(err)
	}
	restarted, err := cloud.NewService(f, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	volumes := restarted.Volumes()
	if len(volumes) != 1 || volumes[0].Status != "Available" {
		t.Fatalf("retained volume: %+v", volumes)
	}
	if err := restarted.AttachVolume(v.ID, "new", "default/recovered", "new-uid"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("retained data missing: %v", err)
	}
	if err := restarted.AttachVolume(v.ID, "other", "default/other", "other-uid"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("double attachment: %v", err)
	}
}

func TestVolumeProvisioningIsIdempotentAndRejectsUnsafeNames(t *testing.T) {
	s, _, _ := newService(t)
	a, err := s.CreateVolume("pvc-123", "demo", "default/data", "1Gi", "Delete")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateVolume("pvc-123", "demo", "default/data", "1Gi", "Delete")
	if err != nil || a != b || len(s.Volumes()) != 1 {
		t.Fatalf("duplicate provisioning: %+v %v", b, err)
	}
	if _, err := s.CreateVolume("../bad", "demo", "default/data", "1Gi", "Delete"); !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("unsafe ID: %v", err)
	}
	if err := s.ReleaseVolumeFor(a.ID, "demo", "default/data", strings.TrimPrefix(a.ID, "pvc-")); err != nil {
		t.Fatal(err)
	}
	if len(s.Volumes()) != 0 {
		t.Fatal("Delete policy retained the volume")
	}
}

func TestDeletePreservesDataWhenStateSaveFails(t *testing.T) {
	s, _, dir := newService(t)
	v, err := s.CreateVolume("pvc-safe", "demo", "default/data", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "demo", "default/data", strings.TrimPrefix(v.ID, "pvc-")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "storage", "volumes", v.ID, "data")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVolume(v.ID); err == nil {
		t.Fatal("delete accepted state save failure")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("data removed despite failed state save: %v", err)
	}
}

func TestStaleReleaseCannotDetachReassignedVolume(t *testing.T) {
	s, _, _ := newService(t)
	v, err := s.CreateVolume("pvc-stale", "old", "default/old", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "old", "default/old", strings.TrimPrefix(v.ID, "pvc-")); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachVolume(v.ID, "new", "default/new", "new-uid"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "old", "default/old", strings.TrimPrefix(v.ID, "pvc-")); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("stale release: %v", err)
	}
	if got := s.Volumes(); got[0].Status != "Bound" || got[0].Cluster != "new" {
		t.Fatalf("reassigned volume changed: %+v", got)
	}
}

func TestRestartCompletesInterruptedVolumeDeletion(t *testing.T) {
	s, f, dir := newService(t)
	v, err := s.CreateVolume("pvc-interrupted", "demo", "default/data", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "demo", "default/data", strings.TrimPrefix(v.ID, "pvc-")); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- dir is a temporary test directory.
	data, err := os.ReadFile(filepath.Join(dir, "state.json")) // #nosec G304 -- temporary test directory.
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"status":"Available"`), []byte(`"status":"Deleting"`), 1)
	// #nosec G703 -- dir is a temporary test directory.
	// #nosec G703 -- temporary test directory.
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := cloud.NewService(f, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Volumes(); len(got) != 0 {
		t.Fatalf("deletion not recovered: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", "volumes", v.ID)); !os.IsNotExist(err) {
		t.Fatalf("directory remains: %v", err)
	}
}

func TestDeletingClusterPreservesVolumesWhenPolicyMayHaveChanged(t *testing.T) {
	s, _, dir := newService(t)
	create(t, s, 1)
	for _, item := range []struct{ id, policy string }{{"pvc-keep", "Retain"}, {"pvc-drop", "Delete"}} {
		if _, err := s.CreateVolume(item.id, "demo", "default/"+item.id, "1Gi", item.policy); err != nil {
			t.Fatal(err)
		}
	}
	op, err := s.DeleteCluster("demo", "student")
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != "DONE" {
		t.Fatalf("delete: %+v", got)
	}
	volumes := s.Volumes()
	if len(volumes) != 2 {
		t.Fatalf("cluster deletion lost volumes: %+v", volumes)
	}
	for _, v := range volumes {
		if v.Status != "Available" || v.Policy != "Retain" {
			t.Fatalf("volume not safely preserved: %+v", v)
		}
		if _, err := os.Stat(filepath.Join(dir, "storage", "volumes", v.ID)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNewVolumeIsWritableByStudentContainers(t *testing.T) {
	s, _, dir := newService(t)
	v, err := s.CreateVolume("pvc-permissions", "demo", "default/data", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "storage", "volumes", v.ID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("volume permissions %o do not permit arbitrary student UIDs", info.Mode().Perm())
	}
}

func TestVolumeDeletionRejectsSymlinkedStorageParent(t *testing.T) {
	s, _, dir := newService(t)
	v, err := s.CreateVolume("pvc-victim", "demo", "default/data", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "demo", "default/data", "victim"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, v.ID)
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "important"), []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "storage", "volumes")
	if err := os.Rename(parent, parent+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVolume(v.ID); err == nil {
		t.Fatal("accepted symlinked storage parent")
	}
	if _, err := os.Stat(filepath.Join(victim, "important")); err != nil {
		t.Fatalf("outside data removed: %v", err)
	}
}

func TestProvisioningRecoversEmptyDirectoryAfterInterruptedCreate(t *testing.T) {
	s, _, dir := newService(t)
	orphan := filepath.Join(dir, "storage", "volumes", "pvc-interrupted-create")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVolume("pvc-interrupted-create", "demo", "default/data", "1Gi", "Retain"); err != nil {
		t.Fatalf("empty interrupted allocation: %v", err)
	}
}

func TestProvisioningDoesNotAdoptNonemptyUnknownDirectory(t *testing.T) {
	s, _, dir := newService(t)
	orphan := filepath.Join(dir, "storage", "volumes", "pvc-unknown")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "data"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVolume("pvc-unknown", "demo", "default/data", "1Gi", "Retain"); err == nil {
		t.Fatal("accepted unknown nonempty directory")
	}
	if _, err := os.Stat(filepath.Join(orphan, "data")); err != nil {
		t.Fatalf("unknown data removed: %v", err)
	}
}

func TestCleanupFailureDoesNotPreventCloudStartup(t *testing.T) {
	s, f, dir := newService(t)
	v, err := s.CreateVolume("pvc-blocked", "demo", "default/data", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseVolumeFor(v.ID, "demo", "default/data", "blocked"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json")) // #nosec G304 -- temporary test directory.
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"status":"Available"`), []byte(`"status":"Deleting"`), 1)
	// #nosec G703 -- temporary test directory.
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "storage", "volumes")
	if err := os.Rename(parent, parent+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), parent); err != nil {
		t.Fatal(err)
	}
	restarted, err := cloud.NewService(f, dir, nil)
	if err != nil {
		t.Fatalf("cloud must start despite storage cleanup failure: %v", err)
	}
	if got := restarted.Volumes(); len(got) != 1 || got[0].Status != "Deleting" {
		t.Fatalf("cleanup state lost: %+v", got)
	}
}

func TestVolumeValidationAndConflict(t *testing.T) {
	s, _, _ := newService(t)
	for _, tc := range []struct{ id, cluster, claim, size, policy string }{
		{"wrong", "demo", "default/data", "1Gi", "Retain"},
		{"pvc-a", "", "default/data", "1Gi", "Retain"},
		{"pvc-a", "demo", "", "1Gi", "Retain"},
		{"pvc-a", "demo", "default/data", "0", "Retain"},
		{"pvc-a", "demo", "default/data", "1Gi", "bad"},
	} {
		if _, err := s.CreateVolume(tc.id, tc.cluster, tc.claim, tc.size, tc.policy); !errors.Is(err, cloud.ErrInvalid) {
			t.Fatalf("accepted %+v: %v", tc, err)
		}
	}
	if _, err := s.CreateVolume("pvc-a", "demo", "default/data", "1Gi", "Retain"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVolume("pvc-a", "other", "default/data", "1Gi", "Retain"); !errors.Is(err, cloud.ErrConflict) {
		t.Fatalf("conflicting identity: %v", err)
	}
	if err := s.AttachVolume("pvc-missing", "demo", "default/data", "id"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("attach missing: %v", err)
	}
	if err := s.DeleteVolume("pvc-missing"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	if err := s.ReleaseVolumeFor("pvc-missing", "demo", "default/data", "id"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("release missing: %v", err)
	}
}
