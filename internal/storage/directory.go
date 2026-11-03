package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
)

var directoryID = regexp.MustCompile(`^pvc-[a-zA-Z0-9-]+$`)

// Directory stores persistent data without imposing a physical write quota.
type Directory struct{ path string }

// NewDirectory selects the private root for persistent volume directories.
func NewDirectory(path string) *Directory { return &Directory{path: path} }

func (d *Directory) open() (*os.Root, error) {
	if err := os.MkdirAll(d.path, 0o700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(d.path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("unsafe storage root")
	}
	root, err := os.OpenRoot(d.path)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(st, actual) {
		_ = root.Close()
		return nil, fmt.Errorf("storage root changed")
	}
	return root, nil
}

// Ensure creates a volume directory while preserving existing contents.
func (d *Directory) Ensure(ctx context.Context, id string, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !directoryID.MatchString(id) || size <= 0 {
		return fmt.Errorf("invalid volume")
	}
	root, err := d.open()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err = root.Mkdir(id, 0o777); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := root.Lstat(id)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("unsafe volume directory")
	}
	f, err := root.Open(id)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	actual, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(st, actual) {
		return fmt.Errorf("volume changed")
	}
	return f.Chmod(0o777)
}

// Delete removes a volume only within the configured storage root.
func (d *Directory) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !directoryID.MatchString(id) {
		return fmt.Errorf("invalid volume")
	}
	root, err := d.open()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	st, err := root.Lstat(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("unsafe volume directory")
	}
	return root.RemoveAll(id)
}
