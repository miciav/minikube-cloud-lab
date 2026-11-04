package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryPersistenceAndConfinement(t *testing.T) {
	root := t.TempDir()
	b := NewDirectory(root)
	ctx := context.Background()
	if err := b.Ensure(ctx, "pvc-test", 32<<20); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pvc-test", "message")
	if err := os.WriteFile(path, []byte("persistent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewDirectory(root).Ensure(ctx, "pvc-test", 32<<20); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- path is inside this test's private temporary directory.
	if data, err := os.ReadFile(path); err != nil || string(data) != "persistent" {
		t.Fatalf("data lost: %s %v", data, err)
	}
	for _, id := range []string{"../escape", "", "pvc/escape"} {
		if err := b.Delete(ctx, id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "pvc-link")); err != nil {
		t.Fatal(err)
	}
	if err := b.Ensure(ctx, "pvc-link", 32<<20); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := b.Delete(ctx, "pvc-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("not deleted: %v", err)
	}
}

func TestDirectoryRejectsCancelledAndInvalidRequests(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage")
	b := NewDirectory(root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Ensure(ctx, "pvc-test", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ensure: %v", err)
	}
	if err := b.Delete(ctx, "pvc-test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled delete: %v", err)
	}
	for _, tc := range []struct {
		id   string
		size int64
	}{{"../escape", 1}, {"pvc-test", 0}, {"pvc-test", -1}} {
		if err := b.Ensure(context.Background(), tc.id, tc.size); err == nil {
			t.Fatalf("accepted invalid request: %+v", tc)
		}
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected requests created storage: %v", err)
	}
}

func TestDirectoryRejectsUnsafeRootsAndVolumeEntries(t *testing.T) {
	for _, kind := range []string{"file-root", "symlink-root", "file-volume", "symlink-volume"} {
		t.Run(kind, func(t *testing.T) {
			base, outside := t.TempDir(), t.TempDir()
			marker := filepath.Join(outside, "keep")
			if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(base, "storage")
			entry := root
			if kind == "file-volume" || kind == "symlink-volume" {
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				entry = filepath.Join(root, "pvc-test")
			}
			var err error
			if kind == "symlink-root" || kind == "symlink-volume" {
				err = os.Symlink(outside, entry)
			} else {
				err = os.WriteFile(entry, []byte("keep"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			b := NewDirectory(root)
			if err := b.Ensure(context.Background(), "pvc-test", 1); err == nil {
				t.Fatal("ensure accepted unsafe path")
			}
			if err := b.Delete(context.Background(), "pvc-test"); err == nil {
				t.Fatal("delete accepted unsafe path")
			}
			if _, err := os.Lstat(entry); err != nil {
				t.Fatalf("unsafe entry was removed: %v", err)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" { // #nosec G304 -- marker is in this test's temporary directory.
				t.Fatalf("outside data changed: %q %v", data, err)
			}
		})
	}
}

func TestDirectoryDeletionIsIdempotentAndDoesNotFollowChildSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	b := NewDirectory(root)
	ctx := context.Background()
	if err := b.Ensure(ctx, "pvc-test", 1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "pvc-test", "link")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := b.Delete(ctx, "pvc-test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside directory removed: %v", err)
	}
}
