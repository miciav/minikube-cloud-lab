package cloud_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func createRegistry(t *testing.T, s *cloud.Service, name string) cloud.Operation {
	t.Helper()
	op, err := s.CreateRegistry(name, "student")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, s, op); op.Status != "DONE" {
		t.Fatalf("create registry: %+v", op)
	}
	return op
}

func registryCalls(f *fake.Infra, method string) []infra.RegistrySpec {
	var out []infra.RegistrySpec
	for _, c := range f.Calls() {
		if c.Method == method {
			out = append(out, c.Registry)
		}
	}
	return out
}

func TestCreateRegistryValidatesName(t *testing.T) {
	s, _, _ := newService(t)
	for _, name := range []string{"Bad_Name", "", strings.Repeat("a", 64)} {
		_, err := s.CreateRegistry(name, "student")
		if !errors.Is(err, cloud.ErrInvalid) || !strings.Contains(err.Error(), "registry name must be a DNS-1123 label") {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

func TestCreateRegistryConflicts(t *testing.T) {
	s, _, _ := newService(t)
	createRegistry(t, s, "corso")
	if _, err := s.CreateRegistry("corso", "student"); !errors.Is(err, cloud.ErrConflict) || !strings.Contains(err.Error(), "registry corso already exists") {
		t.Fatalf("duplicate: %v", err)
	}
	createRegistry(t, s, "due")
	createRegistry(t, s, "tre")
	if _, err := s.CreateRegistry("quattro", "student"); !errors.Is(err, cloud.ErrConflict) || !strings.Contains(err.Error(), "at most 3 registries") {
		t.Fatalf("fourth: %v", err)
	}
}

func TestCreateRegistryRecordsOperation(t *testing.T) {
	s, _, _ := newService(t)
	op := createRegistry(t, s, "corso")
	if op.Type != "CREATE_REGISTRY" || op.Target != "corso" || op.Requester != "student" {
		t.Fatalf("operation: %+v", op)
	}
	got := s.Registries()
	if len(got) != 1 || got[0].Status != "RUNNING" || got[0].Endpoint != "corso.registry:5000" || got[0].IP == "" || got[0].CreatedBy != "student" {
		t.Fatalf("registries: %+v", got)
	}
}

func TestCreateRegistryFailureMarksError(t *testing.T) {
	s, f, _ := newService(t)
	f.Errors["CreateRegistry"] = errors.New("docker down")
	op, err := s.CreateRegistry("corso", "student")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, s, op); op.Status != "ERROR" || s.Registries()[0].Status != "ERROR" {
		t.Fatalf("op %+v registries %+v", op, s.Registries())
	}
}

func TestRegistryCredentialsAreDistinctAndHidden(t *testing.T) {
	s, f, dir := newService(t)
	createRegistry(t, s, "corso")
	push, err := s.RegistryCredentials("corso", true)
	if err != nil {
		t.Fatal(err)
	}
	pull, err := s.RegistryCredentials("corso", false)
	if err != nil {
		t.Fatal(err)
	}
	if push.Username == "" || pull.Username == "" || push.Password == "" || pull.Password == "" || push.Username == pull.Username || push.Password == pull.Password {
		t.Fatalf("push %+v pull %+v", push, pull)
	}
	if push.Endpoint != "corso.registry:5000" {
		t.Fatalf("endpoint %q", push.Endpoint)
	}
	if spec := registryCalls(f, "CreateRegistry")[0]; spec.PushUser != push.Username || spec.PushPassword != push.Password || spec.PullUser != pull.Username || spec.PullPassword != pull.Password {
		t.Fatalf("backend spec %+v", spec)
	}
	if again, _ := s.RegistryCredentials("corso", true); again != push {
		t.Fatalf("unstable: %+v", again)
	}
	reloaded, err := cloud.NewService(f, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := reloaded.RegistryCredentials("corso", false); again != pull {
		t.Fatalf("reload: %+v", again)
	}
	for _, view := range []any{s.Registries(), s.Operations()} {
		data, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), push.Password) || strings.Contains(string(data), pull.Password) {
			t.Fatalf("password leaked: %s", data)
		}
	}
	if _, err := s.RegistryCredentials("missing", true); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestDeleteNonEmptyRegistryNeedsForce(t *testing.T) {
	s, f, _ := newService(t)
	createRegistry(t, s, "corso")
	f.SetRegistryImages("corso", infra.RegistryImage{Repository: "web", Tag: "v1", Bytes: 10})
	images, err := s.RegistryImages(context.Background(), "corso")
	if err != nil || len(images) != 1 || images[0].Repository != "web" {
		t.Fatalf("images %+v %v", images, err)
	}
	if _, err := s.DeleteRegistry("corso", false, "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("non-empty: %v", err)
	}
	op, err := s.DeleteRegistry("corso", true, "student")
	if err != nil {
		t.Fatal(err)
	}
	if op.Type != "DELETE_REGISTRY" || op.Target != "corso" || op.Requester != "student" {
		t.Fatalf("operation: %+v", op)
	}
	if op = wait(t, s, op); op.Status != "DONE" || len(s.Registries()) != 0 || f.RegistryData("corso") {
		t.Fatalf("op %+v registries %+v", op, s.Registries())
	}
	if _, err := s.DeleteRegistry("corso", true, "student"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("gone: %v", err)
	}
}

func TestDeleteEmptyRegistryWithoutForce(t *testing.T) {
	s, f, _ := newService(t)
	createRegistry(t, s, "corso")
	f.Errors["DeleteRegistry"] = errors.New("docker down")
	op, err := s.DeleteRegistry("corso", false, "student")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, s, op); op.Status != "ERROR" || s.Registries()[0].Status != "ERROR" {
		t.Fatalf("op %+v registries %+v", op, s.Registries())
	}
	f.Errors["RegistryImages"] = errors.New("unreachable")
	if _, err := s.DeleteRegistry("corso", false, "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("unlistable: %v", err)
	}
}

func TestDeleteRegistryRejectsBusy(t *testing.T) {
	s, f, _ := newService(t)
	f.Delay = 50 * time.Millisecond
	op, err := s.CreateRegistry("corso", "student")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteRegistry("corso", true, "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("busy: %v", err)
	}
	wait(t, s, op)
}

// seedRegistries starts a service whose snapshot already records registries.
func seedRegistries(t *testing.T, f *fake.Infra, records ...map[string]string) *cloud.Service {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(map[string]any{"registries": records})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := cloud.NewService(f, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStartRegistriesRecreatesMissing(t *testing.T) {
	f := fake.New()
	if _, err := f.CreateRegistry(context.Background(), infra.RegistrySpec{Name: "stray"}); err != nil {
		t.Fatal(err)
	}
	s := seedRegistries(t, f, map[string]string{"name": "corso", "status": "RUNNING", "pushPassword": "pw-push", "pullPassword": "pw-pull"})
	if err := s.StartRegistries(context.Background()); err != nil {
		t.Fatal(err)
	}
	created := registryCalls(f, "CreateRegistry")
	if len(created) != 2 || created[1] != (infra.RegistrySpec{Name: "corso", PushUser: "push", PushPassword: "pw-push", PullUser: "pull", PullPassword: "pw-pull"}) {
		t.Fatalf("created %+v", created)
	}
	if deleted := registryCalls(f, "DeleteRegistry"); len(deleted) != 1 || deleted[0].Name != "stray" {
		t.Fatalf("deleted %+v", deleted)
	}
	observed, _ := f.Registries(context.Background())
	if len(observed) != 1 || observed[0].Name != "corso" {
		t.Fatalf("containers %+v", observed)
	}
	if got := s.Registries(); got[0].Status != "RUNNING" || got[0].IP != observed[0].IP || got[0].Endpoint != "corso.registry:5000" {
		t.Fatalf("registries %+v", got)
	}
}

func TestStartRegistriesReportsFailures(t *testing.T) {
	f := fake.New()
	s := seedRegistries(t, f, map[string]string{"name": "corso", "status": "RUNNING"})
	f.Errors["CreateRegistry"] = errors.New("docker down")
	if err := s.StartRegistries(context.Background()); err == nil || !strings.Contains(err.Error(), "docker down") || s.Registries()[0].Status != "ERROR" {
		t.Fatalf("err %v registries %+v", err, s.Registries())
	}
	f.Errors["Registries"] = errors.New("unreachable")
	if err := s.StartRegistries(context.Background()); err == nil {
		t.Fatal("listing failure ignored")
	}
}

func TestStopRegistriesKeepsRecords(t *testing.T) {
	s, f, _ := newService(t)
	createRegistry(t, s, "corso")
	f.SetRegistryImages("corso", infra.RegistryImage{Repository: "web", Tag: "v1"})
	if err := s.StopRegistries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.Registries(); len(got) != 1 || got[0].Name != "corso" {
		t.Fatalf("registries %+v", got)
	}
	if observed, _ := f.Registries(context.Background()); len(observed) != 0 || !f.RegistryData("corso") {
		t.Fatalf("containers %+v", observed)
	}
}

func TestRegistriesUnavailableWithoutBackend(t *testing.T) {
	s, err := cloud.NewService(struct{ infra.Infra }{fake.New()}, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	const want = "registries are not supported by this backend"
	_, createErr := s.CreateRegistry("corso", "student")
	_, deleteErr := s.DeleteRegistry("corso", true, "student")
	_, imagesErr := s.RegistryImages(context.Background(), "corso")
	for _, err := range []error{createErr, deleteErr, imagesErr} {
		if !errors.Is(err, cloud.ErrUnavailable) || err.Error() != want {
			t.Fatalf("no backend: %v", err)
		}
	}
	if s.StartRegistries(context.Background()) != nil || s.StopRegistries(context.Background()) != nil || len(s.Registries()) != 0 {
		t.Fatal("start/stop without backend must be no-ops")
	}
}
