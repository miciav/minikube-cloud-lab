package cloud

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

// Disk is a blank or student-formatted image owned independently of its instance.
type Disk struct {
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	Instance string `json:"instance,omitempty"`
	Emulated bool   `json:"emulated,omitempty"`
}

type diskGuest interface {
	SetInstanceDisk(context.Context, string, string, bool) error
}

func (s *Service) disksEnabled() bool {
	return os.Getenv("CLOUD_DISK_VOLUME") != ""
}
func (s *Service) diskRoot() string { return filepath.Join(s.dir, "instance-disks") }

func (s *Service) disk(name string) (Disk, error) {
	image := filepath.Join(s.diskRoot(), "images", name)
	st, err := os.Lstat(image)
	if errors.Is(err, os.ErrNotExist) {
		return Disk{}, fmt.Errorf("%w: disk %s", ErrNotFound, name)
	}
	if err != nil {
		return Disk{}, err
	}
	if !st.Mode().IsRegular() {
		return Disk{}, fmt.Errorf("%w: invalid disk image %s", ErrFailedPrecondition, name)
	}
	return Disk{Name: name, Bytes: st.Size(), Instance: s.state.DiskOwners[name]}, nil
}

// InstanceDisks lists the stored images and their attachment reservations.
func (s *Service) InstanceDisks() ([]Disk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.disksEnabled() {
		return nil, unavailable("instance disk storage is not configured")
	}
	files, err := os.ReadDir(filepath.Join(s.diskRoot(), "images"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	result := []Disk{}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".") {
			continue
		}
		d, err := s.disk(f.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, nil
}

// ChangeDisk applies a disk lifecycle request under the instance state lock.
func (s *Service) ChangeDisk(ctx context.Context, action, name, size, instance string) (Disk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.disksEnabled() {
		return Disk{}, unavailable("instance disk storage is not configured")
	}
	if len(validation.IsDNS1123Label(name)) != 0 {
		return Disk{}, fmt.Errorf("%w: invalid disk name", ErrInvalid)
	}
	images := filepath.Join(s.diskRoot(), "images")
	image := filepath.Join(images, name)
	if action == "create" {
		return createDiskImage(images, name, size)
	}
	d, err := s.disk(name)
	if err != nil {
		return Disk{}, err
	}
	if action == "delete" {
		if d.Instance != "" {
			return Disk{}, fmt.Errorf("%w: attached to %s", ErrConflict, d.Instance)
		}
		if err := os.Remove(image); err != nil {
			return Disk{}, err
		}
		return d, syncDiskDir(images)
	}
	if action != "attach" && action != "detach" {
		return Disk{}, ErrInvalid
	}
	return s.changeAttachment(ctx, d, instance, action == "attach", image)
}

func createDiskImage(images, name, size string) (Disk, error) {
	image := filepath.Join(images, name)
	bytes, err := parseStorageBytes(size)
	if err != nil || bytes%512 != 0 {
		return Disk{}, fmt.Errorf("%w: disk size must be a positive multiple of 512 bytes up to 1Ti", ErrInvalid)
	}
	if err := os.MkdirAll(images, 0o700); err != nil {
		return Disk{}, err
	}
	f, err := os.CreateTemp(images, ".create-")
	if err != nil {
		return Disk{}, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	err = f.Truncate(bytes)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return Disk{}, err
	}
	if closeErr != nil {
		return Disk{}, closeErr
	}
	if err = os.Link(f.Name(), image); errors.Is(err, os.ErrExist) {
		return Disk{}, fmt.Errorf("%w: disk already exists", ErrConflict)
	} else if err != nil {
		return Disk{}, err
	}
	return Disk{Name: name, Bytes: bytes}, syncDiskDir(images)
}

func syncDiskDir(path string) error {
	dir, err := os.Open(path) // #nosec G304 -- sync the images directory or a validated instance slot under the cloud data directory.
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func linkDiskImage(image, link string) error {
	if err := os.Link(image, link); errors.Is(err, os.ErrExist) {
		canonical, e := os.Stat(image)
		if e != nil {
			return e
		}
		existing, e := os.Lstat(link)
		if e != nil {
			return e
		}
		if !os.SameFile(canonical, existing) {
			return fmt.Errorf("unexpected file in disk attachment slot")
		}
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Service) changeAttachment(ctx context.Context, d Disk, instance string, attach bool, image string) (Disk, error) {
	name := d.Name
	vm, err := s.state.instance(instance)
	if err != nil {
		return Disk{}, err
	}
	if s.state.instanceBusy(instance) {
		return Disk{}, fmt.Errorf("%w: instance operation in progress", ErrConflict)
	}
	if d.Instance != "" && d.Instance != instance {
		return Disk{}, fmt.Errorf("%w: already attached to %s", ErrConflict, d.Instance)
	}
	guest, ok := s.inf.(diskGuest)
	if !ok {
		return Disk{}, errNoInstances
	}
	link := filepath.Join(s.diskRoot(), "slots", instance, name)
	if vm.Status != "RUNNING" {
		return Disk{}, fmt.Errorf("%w: start the instance before changing disk attachments", ErrConflict)
	}
	if !attach && d.Instance == "" {
		return d, nil
	}
	emulated := os.Getenv("CLOUD_ROOTLESS") == "true"
	if emulated {
		f, err := lockConsoleImage(image, filepath.Dir(link))
		if err != nil {
			return Disk{}, err
		}
		defer func() { _ = f.Close() }()
	}
	if _, err := os.Stat(filepath.Dir(link)); errors.Is(err, os.ErrNotExist) {
		return Disk{}, fmt.Errorf("%w: instance predates disk support; attach to a newly created instance", ErrFailedPrecondition)
	} else if err != nil {
		return Disk{}, err
	}
	// Persist the reservation first: a timed-out guest request may still succeed.
	if d.Instance == "" {
		if err := s.reserveDisk(name, instance); err != nil {
			return Disk{}, err
		}
	}
	// Repair an interrupted attach or detach before asking the guest about this image.
	if err := linkDiskImage(image, link); err != nil {
		return Disk{}, err
	}
	if !emulated {
		if err := guest.SetInstanceDisk(ctx, instance, name, attach); errors.Is(err, infra.ErrDiskBusy) {
			return Disk{}, fmt.Errorf("%w: %w", ErrConflict, err)
		} else if err != nil {
			return Disk{}, err
		}
	}
	if attach {
		d.Instance = instance
		d.Emulated = emulated
		return d, nil
	}
	d.Instance = ""
	return d, s.releaseDisk(name, link)
}

func (s *Service) releaseDisk(name, link string) error {
	if err := os.Remove(link); err != nil {
		return err
	}
	if err := syncDiskDir(filepath.Dir(link)); err != nil {
		return err
	}
	next := s.state.clone()
	delete(next.DiskOwners, name)
	return s.store(next)
}

func (s *Service) reserveDisk(name, instance string) error {
	next := s.state.clone()
	if next.DiskOwners == nil {
		next.DiskOwners = make(map[string]string)
	}
	next.DiskOwners[name] = instance
	return s.store(next)
}

// lockConsoleImage serializes attachment changes with the emulator's flock on the same inode.
func lockConsoleImage(image, slot string) (*os.File, error) {
	if err := os.MkdirAll(slot, 0o700); err != nil {
		return nil, err
	}
	f, err := os.Open(image) // #nosec G304 -- the caller validated the disk name in the cloud image directory.
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: exit the active disk console first", ErrConflict)
		}
		return nil, err
	}
	return f, nil
}
