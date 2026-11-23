package cloud_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

// stoppedSource starts a service with a stopped ubuntu instance "src".
func stoppedSource(t *testing.T) (*cloud.Service, *fake.Infra, string) {
	t.Helper()
	s, f, dir := newService(t)
	launchStopped(t, s, "src", "ubuntu-26.04")
	return s, f, dir
}

func launchStopped(t *testing.T, s *cloud.Service, name, image string) {
	t.Helper()
	op, err := s.CreateInstance(name, image, "lab-small", "student", "")
	finish(t, s, op, err, "DONE")
	op, err = s.StopInstance(name, "student")
	finish(t, s, op, err, "DONE")
}

func capture(t *testing.T, s *cloud.Service, name, from string) {
	t.Helper()
	op, err := s.CreateImage(name, from, "", "student")
	finish(t, s, op, err, "DONE")
}

// editState rewrites the persisted state, as if a previous process left it so.
func editState(t *testing.T, dir string, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, "state.json")
	data, err := os.ReadFile(path) // #nosec G304 -- the test reads its own temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	edit(state)
	if data, err = json.Marshal(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCreateImageCapturesStoppedInstance(t *testing.T) {
	s, f, _ := stoppedSource(t)
	op, err := s.CreateImage("web", "src", "nginx", "student")
	if err != nil || op.Type != "CREATE_IMAGE" || op.Target != "web" || op.Requester != "student" {
		t.Fatalf("accepted: %+v %v", op, err)
	}
	if got, _ := s.Image("web"); got.Status != "CREATING" {
		t.Fatalf("recorded: %+v", got)
	}
	finish(t, s, op, nil, "DONE")
	got, err := s.Image("web")
	if err != nil || got.Status != "AVAILABLE" || got.Source != "src" || got.Base != "ubuntu-26.04" || got.SSHUser != "ubuntu" ||
		got.Bytes != 1<<20 || got.Description != "nginx" || got.CreatedBy != "student" || got.CreatedAt.IsZero() || got.CloudInit {
		t.Fatalf("image: %+v %v", got, err)
	}
	var spec infra.ImageSpec
	for _, call := range f.Calls() {
		if call.Method == "CaptureImage" {
			spec = call.Image
		}
	}
	if spec.Name != "web" || spec.Instance != "src" || spec.SSHUser != "ubuntu" || !strings.HasPrefix(spec.SourceKey, "ssh-ed25519 ") {
		t.Fatalf("capture spec: %+v", spec)
	}
	if builtin, err := s.Image("debian-12"); err != nil || builtin.Name != "debian-12" {
		t.Fatalf("builtin: %+v %v", builtin, err)
	}
}

func TestCreateImageKeepsCapturedCloudInit(t *testing.T) {
	s, f, _ := stoppedSource(t)
	f.ImageCloudInit = true
	capture(t, s, "web", "src")
	if got, _ := s.Image("web"); !got.CloudInit {
		t.Fatalf("cloud-init: %+v", got)
	}
}

func TestCreateImagePreconditions(t *testing.T) {
	s, f, dir := stoppedSource(t)
	op, err := s.CreateInstance("running", "ubuntu-26.04", "lab-small", "student", "")
	finish(t, s, op, err, "DONE")
	launchStopped(t, s, "disked", "ubuntu-26.04")
	// A stopped group member, as the backend and state would record it.
	if _, err := f.CreateInstance(context.Background(), infra.InstanceSpec{Name: "member", GroupID: "g1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.StopInstance(context.Background(), "member"); err != nil {
		t.Fatal(err)
	}
	editState(t, dir, func(state map[string]any) {
		state["diskOwners"] = map[string]any{"data": "disked"}
		state["instances"] = append(state["instances"].([]any), map[string]any{"name": "member", "groupID": "g1", "image": "ubuntu-26.04", "machineType": "lab-small", "status": "STOPPED", "sshUser": "ubuntu", "hostKey": "ssh-ed25519 FAKE"})
	})
	s = reload(t, f, dir)
	capture(t, s, "web", "src")
	cases := []struct {
		name, image, from, description string
		want                           error
	}{
		{"running source", "a", "running", "", cloud.ErrFailedPrecondition},
		{"group member", "a", "member", "", cloud.ErrFailedPrecondition},
		{"attached disk", "a", "disked", "", cloud.ErrFailedPrecondition},
		{"unknown source", "a", "ghost", "", cloud.ErrNotFound},
		{"builtin name", "debian-12", "src", "", cloud.ErrConflict},
		{"duplicate name", "web", "src", "", cloud.ErrConflict},
		{"invalid name", "Web_1", "src", "", cloud.ErrInvalid},
		{"long description", "a", "src", strings.Repeat("x", cloud.MaxImageDescriptionBytes+1), cloud.ErrInvalid},
		{"control character", "a", "src", "line\nbreak", cloud.ErrInvalid},
	}
	for _, c := range cases {
		if _, err := s.CreateImage(c.image, c.from, c.description, "student"); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, err := s.CreateImage("disk", "disked", "", "student"); err == nil || !strings.Contains(err.Error(), "detach its disks first") {
		t.Fatalf("disk message: %v", err)
	}
	op, err = s.CreateImage("ok", "src", strings.Repeat("x", cloud.MaxImageDescriptionBytes), "student")
	finish(t, s, op, err, "DONE")
}

func TestCreateImageLimit(t *testing.T) {
	images := make([]cloud.Image, cloud.MaxImages)
	for i := range images {
		images[i] = cloud.Image{Name: "img-" + string(rune('a'+i)), Status: "AVAILABLE", SSHUser: "ubuntu"}
	}
	s := seedImages(t, images...)
	if _, err := s.CreateImage("ninth", "src", "", "student"); !errors.Is(err, cloud.ErrConflict) {
		t.Fatalf("ninth image: %v", err)
	}
}

func TestImagesUnavailableWithoutBackend(t *testing.T) {
	s, err := cloud.NewService(struct{ infra.Infra }{fake.New()}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() (cloud.Operation, error){
		func() (cloud.Operation, error) { return s.CreateImage("web", "src", "", "student") },
		func() (cloud.Operation, error) { return s.DeleteImage("web", "student") },
	} {
		if _, err := call(); !errors.Is(err, cloud.ErrUnavailable) || err.Error() != "personal images are not supported by this backend" {
			t.Fatalf("no backend: %v", err)
		}
	}
}

func TestSourceLockedDuringCapture(t *testing.T) {
	s, f, _ := stoppedSource(t)
	f.Delay = 200 * time.Millisecond
	op, err := s.CreateImage("web", "src", "", "student")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartInstance("src", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("start while capturing: %v", err)
	}
	if _, err := s.DeleteInstance("src", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("delete while capturing: %v", err)
	}
	if _, err := s.CreateImage("web2", "src", "", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("second capture: %v", err)
	}
	if _, err := s.DeleteImage("web", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("delete while creating: %v", err)
	}
	finish(t, s, op, nil, "DONE")
	f.Delay = 0
	op, err = s.StartInstance("src", "student")
	finish(t, s, op, err, "DONE")
}

func TestCreateImageFailureRecordsError(t *testing.T) {
	s, f, _ := stoppedSource(t)
	f.Errors["CaptureImage"] = errors.New("commit failed")
	op, err := s.CreateImage("web", "src", "", "student")
	finish(t, s, op, err, "ERROR")
	if got, _ := s.Image("web"); got.Status != "ERROR" || got.Error != "commit failed" {
		t.Fatalf("image: %+v", got)
	}
}

func TestCreateImageFailsWithoutSourceKey(t *testing.T) {
	s, _, dir := stoppedSource(t)
	if err := os.WriteFile(filepath.Join(dir, "instances", "src", "id_ed25519"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateImage("web", "src", "", "student"); err == nil {
		t.Fatal("capture accepted with an unreadable key")
	}
	if err := os.Remove(filepath.Join(dir, "instances", "src", "id_ed25519")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateImage("web", "src", "", "student"); err == nil {
		t.Fatal("capture accepted without a key")
	}
	if _, err := s.Image("web"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("record kept: %v", err)
	}
}

func TestCreateImageFromPersonalImageInheritsBase(t *testing.T) {
	s, _, _ := stoppedSource(t)
	capture(t, s, "web", "src")
	launchStopped(t, s, "src2", "web")
	capture(t, s, "web2", "src2")
	if got, _ := s.Image("web2"); got.Base != "ubuntu-26.04" || got.SSHUser != "ubuntu" || got.Source != "src2" || got.CloudInit {
		t.Fatalf("image: %+v", got)
	}
}

func TestImageSurvivesSourceDeletion(t *testing.T) {
	s, _, _ := stoppedSource(t)
	capture(t, s, "web", "src")
	op, err := s.DeleteInstance("src", "student")
	finish(t, s, op, err, "DONE")
	op, err = s.CreateInstance("clone", "web", "lab-small", "student", "")
	finish(t, s, op, err, "DONE")
}

func TestDeleteImageRefusedWhileReferenced(t *testing.T) {
	s, f, dir := stoppedSource(t)
	capture(t, s, "web", "src")
	op, err := s.CreateInstance("clone", "web", "lab-small", "student", "")
	finish(t, s, op, err, "DONE")
	if _, err := s.DeleteImage("web", "student"); !errors.Is(err, cloud.ErrConflict) {
		t.Fatalf("instance reference: %v", err)
	}
	op, err = s.DeleteInstance("clone", "student")
	finish(t, s, op, err, "DONE")
	for _, deleting := range []bool{false, true} {
		editState(t, dir, func(state map[string]any) {
			state["instanceGroups"] = []any{map[string]any{"id": "g1", "name": "svc", "image": "web", "deleting": deleting}}
		})
		s = reload(t, f, dir)
		if _, err := s.DeleteImage("web", "student"); !errors.Is(err, cloud.ErrConflict) {
			t.Fatalf("group reference (deleting=%v): %v", deleting, err)
		}
	}
	if _, err := s.DeleteImage("debian-12", "student"); !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("builtin: %v", err)
	}
	if _, err := s.DeleteImage("ghost", "student"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestDeleteImageRemovesRecord(t *testing.T) {
	s, f, _ := stoppedSource(t)
	capture(t, s, "web", "src")
	op, err := s.DeleteImage("web", "student")
	if err != nil || op.Type != "DELETE_IMAGE" || op.Target != "web" {
		t.Fatalf("accepted: %+v %v", op, err)
	}
	finish(t, s, op, nil, "DONE")
	deleted := false
	for _, call := range f.Calls() {
		deleted = deleted || call.Method == "DeleteImage" && call.Image.Name == "web"
	}
	if !deleted {
		t.Fatalf("backend not asked: %+v", f.Calls())
	}
	if _, err := s.Image("web"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("record kept: %v", err)
	}
}

func TestDeleteImageFailureMarksError(t *testing.T) {
	s, f, _ := stoppedSource(t)
	capture(t, s, "web", "src")
	f.Errors["DeleteImage"] = errors.New("busy")
	op, err := s.DeleteImage("web", "student")
	finish(t, s, op, err, "ERROR")
	if got, _ := s.Image("web"); got.Status != "ERROR" || got.Error != "busy" {
		t.Fatalf("image: %+v", got)
	}
	delete(f.Errors, "DeleteImage")
	op, err = s.DeleteImage("web", "student")
	finish(t, s, op, err, "DONE")
}

func TestReconcileFailsInterruptedImages(t *testing.T) {
	_, f, dir := stoppedSource(t)
	editState(t, dir, func(state map[string]any) {
		state["images"] = []any{
			map[string]any{"name": "web", "source": "src", "status": "CREATING", "sshUser": "ubuntu"},
			map[string]any{"name": "old", "source": "gone", "status": "DELETING", "sshUser": "ubuntu"},
		}
	})
	s := reload(t, f, dir)
	for _, name := range []string{"web", "old"} {
		if got, _ := s.Image(name); got.Status != "ERROR" || got.Error != "interrupted by restart" {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	op, err := s.StartInstance("src", "student")
	finish(t, s, op, err, "DONE")
}

func TestReconcileMarksMissingImages(t *testing.T) {
	s, f, dir := stoppedSource(t)
	capture(t, s, "web", "src")
	capture(t, s, "kept", "src")
	f.ForgetImage("web")
	s = reload(t, f, dir)
	if got, _ := s.Image("web"); got.Status != "ERROR" || got.Error != "image missing from the container runtime" {
		t.Fatalf("missing: %+v", got)
	}
	if got, _ := s.Image("kept"); got.Status != "AVAILABLE" {
		t.Fatalf("kept: %+v", got)
	}
}

func TestReconcileKeepsImagesWhenRuntimeUnobservable(t *testing.T) {
	s, f, dir := stoppedSource(t)
	capture(t, s, "web", "src")
	f.ForgetImage("web")
	f.Errors["ImageExists"] = errors.New("runtime down")
	s = reload(t, f, dir)
	if got, _ := s.Image("web"); got.Status != "AVAILABLE" {
		t.Fatalf("image: %+v", got)
	}
}
