package cloud_test

import (
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
