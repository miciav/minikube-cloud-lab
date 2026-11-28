package cloud

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

// MaxImages bounds personal images, which hold a filesystem copy each.
// ponytail: fixed lab ceiling; raise it if the host disk allows more captures.
const MaxImages = 8

// MaxImageDescriptionBytes bounds the description supplied by a client.
const MaxImageDescriptionBytes = 256

const errNoImages = unavailable("personal images are not supported by this backend")

// capturing reports the image being captured from an instance, which locks the source.
func (st *snapshot) capturing(instance string) (string, bool) {
	for _, image := range st.Images {
		if image.Status == "CREATING" && image.Source == instance {
			return image.Name, true
		}
	}
	return "", false
}

// Image returns a built-in or personal image.
func (s *Service) Image(name string) (Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, image := range builtinImages() {
		if image.Name == name {
			return image, nil
		}
	}
	image, err := s.state.personalImage(name)
	if err != nil {
		return Image{}, err
	}
	return *image, nil
}

// CreateImage captures a stopped standalone instance into a personal image asynchronously.
func (s *Service) CreateImage(name, from, description, requester string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.images == nil {
		return Operation{}, errNoImages
	}
	if len(validation.IsDNS1123Label(name)) > 0 {
		return Operation{}, fmt.Errorf("%w: image name must be a DNS-1123 label", ErrInvalid)
	}
	if len(description) > MaxImageDescriptionBytes || strings.ContainsFunc(description, unicode.IsControl) {
		return Operation{}, fmt.Errorf("%w: description must be at most %d bytes without control characters", ErrInvalid, MaxImageDescriptionBytes)
	}
	if _, err := s.state.image(name); err == nil {
		return Operation{}, fmt.Errorf("%w: image %s already exists", ErrConflict, name)
	}
	if len(s.state.Images) >= MaxImages {
		return Operation{}, fmt.Errorf("%w: at most %d personal images", ErrConflict, MaxImages)
	}
	source, err := s.state.instance(from)
	if err != nil {
		return Operation{}, err
	}
	if source.GroupID != "" {
		return Operation{}, fmt.Errorf("%w: instance %s is managed by a group", ErrFailedPrecondition, from)
	}
	if source.Status != "STOPPED" {
		return Operation{}, fmt.Errorf("%w: instance %s is %s; stop it first", ErrFailedPrecondition, from, source.Status)
	}
	if _, locked := s.state.capturing(from); locked || s.state.instanceBusy(from) {
		return Operation{}, fmt.Errorf("%w: an operation is running on instance %s", ErrFailedPrecondition, from)
	}
	for _, owner := range s.state.DiskOwners {
		if owner == from {
			return Operation{}, fmt.Errorf("%w: instance %s has data disks; detach its disks first", ErrFailedPrecondition, from)
		}
	}
	parent, err := s.state.image(source.Image)
	if err != nil {
		return Operation{}, err
	}
	key, err := s.instanceAuthorizedKey(from)
	if err != nil {
		return Operation{}, err
	}
	next := s.state.clone()
	next.Images = append(next.Images, Image{Name: name, Description: description, SSHUser: source.SSHUser, Source: from, Base: parent.Base, Status: "CREATING", CloudInit: parent.CloudInit, CreatedBy: requester, CreatedAt: time.Now().UTC()})
	op := next.newOperation("CREATE_IMAGE", name, requester)
	if err := s.store(next); err != nil {
		return Operation{}, err
	}
	spec := infra.ImageSpec{Name: name, Instance: from, SSHUser: source.SSHUser, SourceKey: key}
	var captured infra.CapturedImage
	s.launch(op.ID, func() error {
		var err error
		captured, err = s.images.CaptureImage(context.Background(), spec)
		return err
	}, func(st *snapshot, err error) {
		image, lookup := st.personalImage(name)
		if lookup != nil {
			return
		}
		if err != nil {
			image.Status, image.Error = "ERROR", err.Error()
			return
		}
		image.Status, image.Bytes, image.CloudInit = "AVAILABLE", captured.Bytes, captured.CloudInit
	})
	return op, nil
}

// instanceAuthorizedKey derives the authorized_keys line the cloud installed on an instance.
func (s *Service) instanceAuthorizedKey(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.instanceDir(name), "id_ed25519")) // #nosec G304 -- instance keys live in the data directory.
	if err != nil {
		return "", fmt.Errorf("read instance key: %w", err)
	}
	private, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return "", fmt.Errorf("parse instance key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return "", fmt.Errorf("parse instance key: %w", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}

// DeleteImage removes an unreferenced personal image asynchronously.
func (s *Service) DeleteImage(name, requester string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.images == nil {
		return Operation{}, errNoImages
	}
	if slices.ContainsFunc(builtinImages(), func(i Image) bool { return i.Name == name }) {
		return Operation{}, fmt.Errorf("%w: built-in image %s cannot be deleted", ErrInvalid, name)
	}
	image, err := s.state.personalImage(name)
	if err != nil {
		return Operation{}, err
	}
	if image.Status == "CREATING" || image.Status == "DELETING" {
		return Operation{}, fmt.Errorf("%w: image %s is %s", ErrFailedPrecondition, name, image.Status)
	}
	if slices.ContainsFunc(s.state.Instances, func(i Instance) bool { return i.Image == name }) ||
		slices.ContainsFunc(s.state.Groups, func(g Group) bool { return g.Image == name }) {
		return Operation{}, fmt.Errorf("%w: image %s is used by an instance or group", ErrConflict, name)
	}
	next := s.state.clone()
	marked, _ := next.personalImage(name)
	marked.Status = "DELETING"
	op := next.newOperation("DELETE_IMAGE", name, requester)
	if err := s.store(next); err != nil {
		return Operation{}, err
	}
	s.launch(op.ID, func() error {
		return s.images.DeleteImage(context.Background(), name)
	}, func(st *snapshot, err error) {
		if err == nil {
			st.Images = slices.DeleteFunc(st.Images, func(i Image) bool { return i.Name == name })
		} else if image, lookup := st.personalImage(name); lookup == nil {
			image.Status, image.Error = "ERROR", err.Error()
		}
	})
	return op, nil
}

// reconcileImages fails captures and deletions a restart interrupted and flags pruned images.
func (s *Service) reconcileImages() {
	if s.images == nil {
		return
	}
	for i := range s.state.Images {
		if image := &s.state.Images[i]; image.Status == "CREATING" || image.Status == "DELETING" {
			image.Status, image.Error = "ERROR", "interrupted by restart"
		}
	}
	for i := range s.state.Images {
		image := &s.state.Images[i]
		if image.Status != "AVAILABLE" {
			continue
		}
		exists, err := s.images.ImageExists(context.Background(), image.Name)
		if err != nil {
			s.logger.Error("cannot observe personal images; availability preserved", "error", err)
			return
		}
		if !exists {
			image.Status, image.Error = "ERROR", "image missing from the container runtime"
		}
	}
}
