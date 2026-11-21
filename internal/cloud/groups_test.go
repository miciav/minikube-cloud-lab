package cloud_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func TestGroupPersistsTemplateAndDesiredCapacity(t *testing.T) {
	s, backend, dir := newService(t)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"instances":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := cloud.NewService(backend, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := cloud.GroupSpec{Name: "web", Image: "debian-12", MachineType: "lab-small",
		Bootstrap: "#!/bin/sh\ntrue", Min: 1, Desired: 2, Max: 4, Port: 8080, HealthPath: "/health", TargetRPS: 40}
	if _, err := s.CreateGroup(spec); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("create with module off: %v", err)
	}
	if err := s.ActivateInstanceAutoscaling(); err != nil {
		t.Fatal(err)
	}
	invalid := spec
	invalid.Min = 3
	if _, err := s.CreateGroup(invalid); !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("min > desired: %v", err)
	}
	g, err := s.CreateGroup(spec)
	if err != nil || g.ID == "" || g.TemplateVersion != 1 {
		t.Fatalf("create: %+v, %v", g, err)
	}
	if _, err := s.CreateGroup(spec); !errors.Is(err, cloud.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.ResizeGroup(g.ID, 5, "student", ""); !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("desired > max: %v", err)
	}
	for range 2 {
		if _, err := s.ResizeGroup(g.ID, 3, "student", ""); err != nil {
			t.Fatal(err)
		}
	}
	s, err = cloud.NewService(backend, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Group("web")
	if err != nil || got.ID != g.ID || got.Desired != 3 || got.Bootstrap != spec.Bootstrap {
		t.Fatalf("reload: %+v, %v", got, err)
	}
	if ops := s.Operations(); len(ops) != 1 || ops[0].Type != "RESIZE_GROUP" || ops[0].Status != "DONE" {
		t.Fatalf("resize history after retry: %+v", ops)
	}
}
