package storage

import (
	"context"
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
