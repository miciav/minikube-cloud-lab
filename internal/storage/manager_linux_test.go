package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestManagerBoundsAndRecoversVolume(t *testing.T) {
	if os.Getenv("CLOUD_STORAGE_MOUNT_TEST") != "1" {
		t.Skip("requires isolated Linux mount namespace and loop devices")
	}
	dir := t.TempDir()
	mounts := filepath.Join(dir, "mounts")
	cfg := filepath.Join(dir, "ganesha.conf")
	m, err := newManager(filepath.Join(dir, "backing"), mounts, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.Ensure(ctx, "pvc-a", 64<<20); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(ctx, "pvc-b", 64<<20); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(ctx, "pvc-a", 128<<20); err == nil {
		t.Fatal("silently resized existing volume")
	}
	// #nosec G304 -- isolated test volume path.
	f, err := os.Create(filepath.Join(mounts, "pvc-a", "fill"))
	if err != nil {
		t.Fatal(err)
	}
	full := false
	for i := 0; i < 70; i++ {
		_, err = f.Write(make([]byte, 1<<20))
		if err != nil {
			if !errors.Is(err, syscall.ENOSPC) {
				t.Fatal(err)
			}
			full = true
			break
		}
	}
	if !full {
		t.Fatal("volume exceeded capacity")
	}
	_ = f.Close()
	if err := os.WriteFile(filepath.Join(mounts, "pvc-b", "sentinel"), []byte("persistent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m, err = newManager(filepath.Join(dir, "backing"), mounts, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.close() }()
	// #nosec G304 -- isolated test volume path.
	b, err := os.ReadFile(filepath.Join(mounts, "pvc-b", "sentinel"))
	if err != nil || string(b) != "persistent" {
		t.Fatalf("recovery %q %v", b, err)
	}
	if err := m.Delete(ctx, "pvc-a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "pvc-a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(ctx, "../escape", 64<<20); err == nil {
		t.Fatal("unsafe ID accepted")
	}
}

func TestManagerRetryPreservesFormattedImage(t *testing.T) {
	if os.Getenv("CLOUD_STORAGE_MOUNT_TEST") != "1" {
		t.Skip("requires isolated Linux mounts")
	}
	dir := t.TempDir()
	mounts := filepath.Join(dir, "mounts")
	backing := filepath.Join(dir, "backing")
	m, err := newManager(backing, mounts, filepath.Join(dir, "ganesha.conf"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.close() }()
	path := filepath.Join(mounts, "pvc-retry")
	if err = os.Symlink(t.TempDir(), path); err != nil {
		t.Fatal(err)
	}
	if err = m.Ensure(context.Background(), "pvc-retry", 64<<20); err == nil {
		t.Fatal("unsafe mount accepted")
	}
	images, err := os.ReadDir(filepath.Join(backing, "images"))
	if err != nil || len(images) != 1 {
		t.Fatalf("images %v %v", images, err)
	}
	before, err := os.Stat(filepath.Join(backing, "images", images[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = m.Ensure(context.Background(), "pvc-retry", 64<<20); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(backing, "images", images[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("retry replaced an existing formatted image")
	}
}
