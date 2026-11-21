// Package atomicfile replaces files so readers see either the old or the new content.
package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
)

// Write durably replaces path with a private (0600) file holding data.
func Write(path string, data []byte) error {
	return WriteMode(path, data, 0o600)
}

// WriteMode sets the requested permissions before publishing the replacement.
func WriteMode(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	// #nosec G304 -- the caller owns the target directory.
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
