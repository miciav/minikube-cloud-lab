package cloud_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func createInstance(t *testing.T, s *cloud.Service, name string) {
	t.Helper()
	op, err := s.CreateInstance(name, "debian-12", "lab-small", "student")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, s, op); op.Status != "DONE" {
		t.Fatalf("create instance: %+v", op)
	}
}

func TestImagesCatalog(t *testing.T) {
	want := []cloud.Image{{Name: "debian-12", Description: "Debian 12 (minikube kicbase) with Docker Engine", SSHUser: "docker"}}
	if got := cloud.Images(); !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog: %+v", got)
	}
	if _, err := cloud.LookupImage("ubuntu"); !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("unknown image: %v", err)
	}
}

func TestCreateInstanceRecordsOperationAndKey(t *testing.T) {
	s, f, dir := newService(t)
	op, err := s.CreateInstance("vm1", "debian-12", "lab-small", "student")
	if err != nil || op.Type != "CREATE_INSTANCE" || op.Target != "vm1" || op.Requester != "student" || op.Status != "RUNNING" {
		t.Fatalf("accepted: %+v %v", op, err)
	}
	if done := wait(t, s, op); done.Status != "DONE" {
		t.Fatalf("operation: %+v", done)
	}
	got, err := s.Instance("vm1")
	if err != nil || got.Status != "RUNNING" || got.IP != "172.30.0.200" || got.HostKey != "ssh-ed25519 FAKE-vm1" || got.SSHUser != "docker" || got.MachineType != "lab-small" || got.Image != "debian-12" {
		t.Fatalf("instance: %+v %v", got, err)
	}
	key := filepath.Join(dir, "instances", "vm1", "id_ed25519")
	info, err := os.Stat(key)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", info, err)
	}
	data, _ := os.ReadFile(key)
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	var spec infra.InstanceSpec
	for _, c := range f.Calls() {
		if c.Method == "CreateInstance" {
			spec = c.Instance
		}
	}
	if spec.AuthorizedKey != authorized || spec.Shape != (infra.MachineShape{CPUs: 2, MemoryMiB: 2048}) || spec.SSHUser != "docker" || spec.Image != "debian-12" {
		t.Fatalf("backend spec: %+v", spec)
	}
	state, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if strings.Contains(string(state), "PRIVATE KEY") {
		t.Fatal("state.json holds the private key")
	}
}

func TestCreateInstanceValidation(t *testing.T) {
	s, _, _ := newService(t)
	for _, tc := range []struct{ name, image, kind string }{{"Bad_Name", "debian-12", "lab-small"}, {"vm1", "ubuntu", "lab-small"}, {"vm1", "debian-12", "huge"}} {
		if _, err := s.CreateInstance(tc.name, tc.image, tc.kind, "student"); !errors.Is(err, cloud.ErrInvalid) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	createInstance(t, s, "vm1")
	if _, err := s.CreateInstance("vm1", "debian-12", "lab-small", "student"); !errors.Is(err, cloud.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	for i := 2; i <= cloud.MaxInstances; i++ {
		createInstance(t, s, fmt.Sprintf("vm%d", i))
	}
	before := len(s.Operations())
	if _, err := s.CreateInstance("vm9", "debian-12", "lab-small", "student"); !errors.Is(err, cloud.ErrConflict) {
		t.Fatalf("limit: %v", err)
	}
	if len(s.Operations()) != before {
		t.Fatal("a rejected request recorded an operation")
	}
}

func TestCreateInstanceFailureKeepsRecord(t *testing.T) {
	s, f, dir := newService(t)
	f.Errors["CreateInstance"] = errors.New("boom")
	op, err := s.CreateInstance("vm1", "debian-12", "lab-small", "student")
	if err != nil {
		t.Fatal(err)
	}
	if done := wait(t, s, op); done.Status != "ERROR" || done.Error != "boom" {
		t.Fatalf("operation: %+v", done)
	}
	if got, _ := s.Instance("vm1"); got.Status != "ERROR" {
		t.Fatalf("instance: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "instances", "vm1", "id_ed25519")); err != nil {
		t.Fatalf("key removed: %v", err)
	}
}

func TestInstanceNameMayMatchCluster(t *testing.T) {
	s, f, _ := newService(t)
	f.Delay = 100 * time.Millisecond
	if _, err := s.CreateCluster("demo", "lab-small", 1, 3, "student"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInstance("demo", "debian-12", "lab-small", "student"); err != nil {
		t.Fatalf("instance named like the cluster: %v", err)
	}
}

func TestInstancesUnavailableWithoutBackend(t *testing.T) {
	s, err := cloud.NewService(struct{ infra.Infra }{fake.New()}, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInstance("vm1", "debian-12", "lab-small", "student"); !errors.Is(err, cloud.ErrUnavailable) {
		t.Fatalf("no backend: %v", err)
	}
}

func finish(t *testing.T, s *cloud.Service, op cloud.Operation, err error, status cloud.OpStatus) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if done := wait(t, s, op); done.Status != status {
		t.Fatalf("operation: %+v", done)
	}
}

func TestStopStartInstance(t *testing.T) {
	s, _, _ := newService(t)
	createInstance(t, s, "vm1")
	op, err := s.StopInstance("vm1", "student")
	if op.Type != "STOP_INSTANCE" {
		t.Fatalf("stop type: %+v", op)
	}
	finish(t, s, op, err, "DONE")
	if got, _ := s.Instance("vm1"); got.Status != "STOPPED" {
		t.Fatalf("stopped: %+v", got)
	}
	if _, err := s.StopInstance("vm1", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("stop twice: %v", err)
	}
	op, err = s.StartInstance("vm1", "student")
	if op.Type != "START_INSTANCE" {
		t.Fatalf("start type: %+v", op)
	}
	finish(t, s, op, err, "DONE")
	if got, _ := s.Instance("vm1"); got.Status != "RUNNING" || got.IP != "172.30.0.200" {
		t.Fatalf("started: %+v", got)
	}
	if _, err := s.StartInstance("vm1", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("start twice: %v", err)
	}
	for _, call := range []func(string, string) (cloud.Operation, error){s.StartInstance, s.StopInstance, s.DeleteInstance} {
		if _, err := call("missing", "student"); !errors.Is(err, cloud.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
	}
}

func TestInstanceBusyRejected(t *testing.T) {
	s, f, _ := newService(t)
	f.Delay = 200 * time.Millisecond
	if _, err := s.CreateInstance("vm1", "debian-12", "lab-small", "student"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StopInstance("vm1", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("stop while creating: %v", err)
	}
	if _, err := s.DeleteInstance("vm1", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("delete while creating: %v", err)
	}
}

func TestDeleteInstanceRemovesRecordAndKey(t *testing.T) {
	s, f, dir := newService(t)
	createInstance(t, s, "vm1")
	op, err := s.DeleteInstance("vm1", "student")
	if got, _ := s.Instance("vm1"); got.Status != "DELETING" || op.Type != "DELETE_INSTANCE" {
		t.Fatalf("accepted: %+v %+v", got, op)
	}
	finish(t, s, op, err, "DONE")
	if _, err := s.Instance("vm1"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("record kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "instances", "vm1")); !os.IsNotExist(err) {
		t.Fatalf("key directory kept: %v", err)
	}
	if left, _ := f.Instances(context.Background()); len(left) != 0 {
		t.Fatalf("backend kept: %+v", left)
	}
}

func TestDeleteInstanceAfterManualRemoval(t *testing.T) {
	s, f, _ := newService(t)
	createInstance(t, s, "vm1")
	if err := f.DeleteInstance(context.Background(), "vm1"); err != nil {
		t.Fatal(err)
	}
	op, err := s.DeleteInstance("vm1", "student")
	finish(t, s, op, err, "DONE")
}

func TestDeleteFailureMarksError(t *testing.T) {
	s, f, dir := newService(t)
	createInstance(t, s, "vm1")
	f.Errors["DeleteInstance"] = errors.New("boom")
	op, err := s.DeleteInstance("vm1", "student")
	finish(t, s, op, err, "ERROR")
	if got, _ := s.Instance("vm1"); got.Status != "ERROR" {
		t.Fatalf("instance: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "instances", "vm1", "id_ed25519")); err != nil {
		t.Fatalf("key removed: %v", err)
	}
	delete(f.Errors, "DeleteInstance")
	op, err = s.DeleteInstance("vm1", "student")
	finish(t, s, op, err, "DONE")
}

func TestStartFromErrorAfterFailedStop(t *testing.T) {
	s, f, _ := newService(t)
	createInstance(t, s, "vm1")
	f.Errors["StopInstance"] = errors.New("boom")
	op, err := s.StopInstance("vm1", "student")
	finish(t, s, op, err, "ERROR")
	delete(f.Errors, "StopInstance")
	op, err = s.StartInstance("vm1", "student")
	finish(t, s, op, err, "DONE")
	if got, _ := s.Instance("vm1"); got.Status != "RUNNING" {
		t.Fatalf("instance: %+v", got)
	}
}
