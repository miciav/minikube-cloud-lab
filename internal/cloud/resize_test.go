package cloud_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func TestResizeDurabilityAndBounds(t *testing.T) {
	s, f, dir := newService(t)
	if _, err := s.ResizeCluster("missing", 3, "student"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	create(t, s, 1)
	before, _ := s.Cluster("demo")
	stale := before.CreatedAt.Add(-time.Hour)
	if _, err := s.ResizeClusterIfUnchanged("demo", 3, "student", &stale); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatal("stale generation accepted", err)
	}
	for _, n := range []int{-1, 0, cloud.MaxMachines + 1} {
		if _, err := s.ResizeCluster("demo", n, "student"); !errors.Is(err, cloud.ErrInvalid) {
			t.Fatal("invalid size accepted", n, err)
		}
	}
	ops, err := s.ResizeClusterIfUnchanged("demo", 3, "student", &before.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if result := wait(t, s, op); result.Status != "DONE" {
			t.Fatal(result)
		}
	}
	restarted, err := cloud.NewService(f, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := restarted.Cluster("demo")
	if c.MinMachines != 3 || c.MaxMachines != 3 || !c.CreatedAt.Equal(before.CreatedAt) {
		t.Fatal("size or identity lost after restart", c)
	}
	if _, err := restarted.DeleteMachine("demo", "demo-m02", "cluster-autoscaler"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatal("new minimum not enforced", err)
	}
	if _, err := restarted.AddMachines("demo", 1, "cluster-autoscaler"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatal("new maximum not enforced", err)
	}
}

func TestResizeRejectsOverlappingOperations(t *testing.T) {
	s, f, _ := newService(t)
	f.Delay = 30 * time.Millisecond
	op, err := s.CreateCluster("demo", "lab-small", 1, 1, "student")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResizeCluster("demo", 3, "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatal("accepted resize during create", err)
	}
	wait(t, s, op)
	ops, err := s.ResizeCluster("demo", 3, "student")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResizeCluster("demo", 4, "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatal("accepted overlapping resize", err)
	}
	if _, err := s.DeleteCluster("demo", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatal("accepted deletion during resize", err)
	}
	for _, op := range ops {
		wait(t, s, op)
	}
}

func TestResizeSaveFailureIsAtomic(t *testing.T) {
	s, f, dir := newService(t)
	create(t, s, 1)
	before, _ := s.Cluster("demo")
	path := filepath.Join(dir, "state.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResizeCluster("demo", 3, "student"); err == nil {
		t.Fatal("ignored persistence failure")
	}
	after, _ := s.Cluster("demo")
	m, _ := s.Machines("demo")
	if before != after || len(m) != 1 || len(s.Operations()) != 1 {
		t.Fatal("failed snapshot changed live state", after, m)
	}
	for _, call := range f.Calls() {
		if call.Method == "AddMachine" {
			t.Fatal("worker started before persistence")
		}
	}
}

func TestResizeFailureCanRetryWithoutDeletingCluster(t *testing.T) {
	s, f, _ := newService(t)
	create(t, s, 1)
	f.Errors["AddMachine"] = errors.New("worker creation failed")
	ops, err := s.ResizeCluster("demo", 3, "student")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if result := wait(t, s, op); result.Status != "ERROR" {
			t.Fatal(result)
		}
	}
	delete(f.Errors, "AddMachine")
	ops, err = s.ResizeCluster("demo", 3, "student")
	if err != nil || len(ops) != 2 {
		t.Fatal("retry", ops, err)
	}
	for _, op := range ops {
		if result := wait(t, s, op); result.Status != "DONE" {
			t.Fatal(result)
		}
	}
	m, _ := s.Machines("demo")
	if len(m) != 3 {
		t.Fatal(m)
	}
}

func TestResizeFixesLimitsWithoutAddingAnExistingNode(t *testing.T) {
	s, f, _ := newService(t)
	create(t, s, 1)
	ops, err := s.ResizeCluster("demo", 1, "student")
	c, _ := s.Cluster("demo")
	if err != nil || len(ops) != 0 || c.MinMachines != 1 || c.MaxMachines != 1 {
		t.Fatal(ops, err, c)
	}
	for _, call := range f.Calls() {
		if call.Method == "AddMachine" {
			t.Fatal("unexpected worker")
		}
	}
}
