package cloud_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func newService(t *testing.T) (*cloud.Service, *fake.Infra, string) {
	t.Helper()
	f := fake.New()
	dir := t.TempDir()
	s, err := cloud.NewService(f, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s, f, dir
}

func wait(t *testing.T, s *cloud.Service, op cloud.Operation) cloud.Operation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.Wait(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func create(t *testing.T, s *cloud.Service, minM int) {
	t.Helper()
	op, err := s.CreateCluster("demo", "lab-small", minM, 3, "student")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, s, op); op.Status != "DONE" {
		t.Fatalf("create: %+v", op)
	}
}

func TestCreateClusterRecordsOperation(t *testing.T) {
	s, _, _ := newService(t)
	op, err := s.CreateCluster("demo", "lab-small", 1, 3, "student")
	if err != nil || op.Status != "RUNNING" || op.Requester != "student" {
		t.Fatalf("accepted: %+v %v", op, err)
	}
	result := wait(t, s, op)
	if result.Status != "DONE" || result.EndedAt == nil {
		t.Fatalf("completion: %+v", result)
	}
	c, err := s.Cluster("demo")
	if err != nil || c.Status != "RUNNING" {
		t.Fatalf("cluster: %+v %v", c, err)
	}
	m, err := s.Machines("demo")
	if err != nil || len(m) != 1 || !m[0].ControlPlane || m[0].CreatedBy != "student" {
		t.Fatalf("machines: %+v %v", m, err)
	}
	data, err := s.Kubeconfig("demo")
	if err != nil || len(data) == 0 {
		t.Fatalf("credentials: %s %v", data, err)
	}
}

func TestCreateConflict(t *testing.T) {
	s, f, _ := newService(t)
	create(t, s, 1)
	if _, err := s.CreateCluster("other", "lab-small", 1, 3, "student"); !errors.Is(err, cloud.ErrConflict) || !strings.Contains(err.Error(), "demo") {
		t.Fatalf("conflict: %v", err)
	}
	count := 0
	for _, c := range f.Calls() {
		if c.Method == "CreateCluster" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("create requests: %d", count)
	}
}

func TestValidatesSizes(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		min, max   int
	}{{"demo", "lab-small", 0, 3}, {"demo", "lab-small", 2, 1}, {"Not DNS!", "lab-small", 1, 3}, {"demo", "unknown", 1, 3}} {
		t.Run(fmt.Sprintf("%s-%s-%d", tc.name, tc.kind, tc.min), func(t *testing.T) {
			s, _, _ := newService(t)
			if _, err := s.CreateCluster(tc.name, tc.kind, tc.min, tc.max, "student"); !errors.Is(err, cloud.ErrInvalid) {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}

func TestCreateClusterPassesShapeAndLabels(t *testing.T) {
	s, f, _ := newService(t)
	op, err := s.CreateCluster("demo", "lab-medium", 1, 3, "student")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	call := f.Calls()[0]
	if call.Spec.Shape.CPUs != 4 || call.Spec.Shape.MemoryMiB != 8192 || call.Spec.KubernetesVersion != "v1.35.6" {
		t.Fatalf("shape: %+v", call.Spec)
	}
	for k, v := range map[string]string{"node.kubernetes.io/instance-type": "lab-medium", "topology.kubernetes.io/region": "lab-region-1", "topology.kubernetes.io/zone": "lab-region-1-a"} {
		if call.Spec.NodeLabels[k] != v {
			t.Fatalf("label %s: %q", k, call.Spec.NodeLabels[k])
		}
	}
}

func TestAddMachinesRespectsMax(t *testing.T) {
	s, _, _ := newService(t)
	create(t, s, 1)
	for _, n := range []int{0, -1, 3} {
		if _, err := s.AddMachines("demo", n, "cluster-autoscaler"); err == nil {
			t.Fatalf("accepted increase %d", n)
		}
	}
	ops, err := s.AddMachines("demo", 2, "cluster-autoscaler")
	if err != nil || len(ops) != 2 {
		t.Fatalf("add: %+v %v", ops, err)
	}
	for _, op := range ops {
		if result := wait(t, s, op); result.Status != "DONE" || result.Requester != "cluster-autoscaler" {
			t.Fatalf("add completion: %+v", result)
		}
	}
	m, err := s.Machines("demo")
	if err != nil || len(m) != 3 {
		t.Fatalf("machines: %+v %v", m, err)
	}
}

func TestDeleteMachineProtectsControlPlaneAndMin(t *testing.T) {
	s, _, _ := newService(t)
	create(t, s, 2)
	if _, err := s.DeleteMachine("demo", "demo", "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("control plane: %v", err)
	}
	machines, _ := s.Machines("demo")
	worker := machines[1].Name
	if _, err := s.DeleteMachine("demo", worker, "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("minimum: %v", err)
	}
	ops, err := s.AddMachines("demo", 1, "student")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, ops[0])
	op, err := s.DeleteMachine("demo", worker, "student")
	if err != nil {
		t.Fatal(err)
	}
	if result := wait(t, s, op); result.Status != "DONE" {
		t.Fatalf("delete: %+v", result)
	}
	machines, _ = s.Machines("demo")
	if len(machines) != 2 {
		t.Fatalf("remaining: %+v", machines)
	}
}

func TestInfraFailureMarksOperationError(t *testing.T) {
	s, f, _ := newService(t)
	f.Errors["CreateCluster"] = errors.New("Docker unavailable")
	op, err := s.CreateCluster("demo", "lab-small", 1, 3, "student")
	if err != nil {
		t.Fatal(err)
	}
	result := wait(t, s, op)
	if result.Status != "ERROR" || !strings.Contains(result.Error, "Docker unavailable") {
		t.Fatalf("failure: %+v", result)
	}
	c, _ := s.Cluster("demo")
	if c.Status != "ERROR" {
		t.Fatalf("failed cluster: %+v", c)
	}
}

func TestStatePersistsAcrossRestart(t *testing.T) {
	s, f, dir := newService(t)
	create(t, s, 1)
	restarted, err := cloud.NewService(f, dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if c, err := restarted.Cluster("demo"); err != nil || c.Status != "RUNNING" {
		t.Fatalf("restart: %+v %v", c, err)
	}
	if len(restarted.Operations()) != 1 {
		t.Fatal("lost operation history")
	}
}

func TestServiceReconcilesAfterRestart(t *testing.T) {
	s, f, dir := newService(t)
	create(t, s, 1)
	ops, err := s.AddMachines("demo", 1, "student")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, ops[0])
	machines, _ := s.Machines("demo")
	missing := machines[1].Name
	if err := f.DeleteMachine(context.Background(), "demo", missing); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	data, err := os.ReadFile(path) // #nosec G304 -- test fixture under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	var disk struct {
		Clusters   []cloud.Cluster   `json:"clusters"`
		Machines   []cloud.Machine   `json:"machines"`
		Operations []cloud.Operation `json:"operations"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	disk.Operations[1].Status = "RUNNING"
	disk.Operations[1].EndedAt = nil
	data, err = json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := cloud.NewService(f, dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := restarted.Operation(ops[0].ID)
	if err != nil || interrupted.Status != "ERROR" || interrupted.Error != "interrupted by restart" {
		t.Fatalf("interrupted: %+v %v", interrupted, err)
	}
	machines, err = restarted.Machines("demo")
	if err != nil || len(machines) != 1 {
		t.Fatalf("reconcile: %+v %v", machines, err)
	}
	noted := false
	for _, op := range restarted.Operations() {
		if strings.Contains(op.Error, missing) {
			noted = true
		}
	}
	if !noted {
		t.Fatal("missing machine absent from operation log")
	}
}

func TestOperationsNewestFirst(t *testing.T) {
	s, _, _ := newService(t)
	create(t, s, 1)
	op, err := s.DeleteCluster("demo", "student")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	ops := s.Operations()
	if len(ops) != 2 || ops[0].Type != "DELETE_CLUSTER" || len(s.Clusters()) != 0 {
		t.Fatalf("operations: %+v", ops)
	}
}

func TestMissingResources(t *testing.T) {
	s, _, _ := newService(t)
	for _, get := range []func() error{func() error { _, err := s.Cluster("missing"); return err }, func() error { _, err := s.Machines("missing"); return err }, func() error { _, err := s.Operation("missing"); return err }, func() error { _, err := s.Kubeconfig("missing"); return err }, func() error { _, err := s.DeleteCluster("missing", "student"); return err }, func() error { _, err := s.AddMachines("missing", 1, "student"); return err }, func() error { _, err := s.DeleteMachine("missing", "node", "student"); return err }} {
		if err := get(); !errors.Is(err, cloud.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
	}
}

func TestStateErrorsDoNotStartInfrastructure(t *testing.T) {
	s, f, dir := newService(t)
	if err := os.Mkdir(filepath.Join(dir, "state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCluster("demo", "lab-small", 1, 3, "student"); err == nil {
		t.Fatal("accepted state persistence failure")
	}
	if len(s.Clusters()) != 0 || len(f.Calls()) != 0 {
		t.Fatal("failed persistence started a cluster")
	}
	bad := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(bad, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cloud.NewService(f, filepath.Dir(bad), slog.Default()); err == nil {
		t.Fatal("accepted corrupt state")
	}
}

type serializedInfra struct {
	infra.Infra
	active, max atomic.Int32
}

func (f *serializedInfra) AddMachine(ctx context.Context, name string) (infra.Machine, error) {
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		old := f.max.Load()
		if n <= old || f.max.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	return f.Infra.AddMachine(ctx, name)
}

func TestInfrastructureMutationsAreSerialized(t *testing.T) {
	f := &serializedInfra{Infra: fake.New()}
	s, err := cloud.NewService(f, t.TempDir(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	create(t, s, 1)
	ops, err := s.AddMachines("demo", 2, "student")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		wait(t, s, op)
	}
	if f.max.Load() != 1 {
		t.Fatal("overlapping infrastructure mutations")
	}
}
