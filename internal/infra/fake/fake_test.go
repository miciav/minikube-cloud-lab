package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func TestLifecycle(t *testing.T) {
	f := fake.New()
	ctx := context.Background()
	if err := f.CreateCluster(ctx, infra.ClusterSpec{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	machines, err := f.Machines(ctx, "demo")
	if err != nil || len(machines) != 1 || !machines[0].ControlPlane {
		t.Fatalf("initial: %v %v", machines, err)
	}
	worker, err := f.AddMachine(ctx, "demo")
	if err != nil || worker.ControlPlane {
		t.Fatalf("worker: %v %v", worker, err)
	}
	machines, err = f.Machines(ctx, "demo")
	if err != nil || len(machines) != 2 {
		t.Fatalf("added: %v %v", machines, err)
	}
	data, err := f.Kubeconfig(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.Load(data)
	if err != nil || cfg.CurrentContext != "demo" {
		t.Fatalf("credentials: %s %v", data, err)
	}
	if err := f.DeleteMachine(ctx, "demo", worker.Name); err != nil {
		t.Fatal(err)
	}
	machines, err = f.Machines(ctx, "demo")
	if err != nil || len(machines) != 1 {
		t.Fatalf("deleted: %v %v", machines, err)
	}
	if err := f.DeleteCluster(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	machines, err = f.Machines(ctx, "demo")
	if err != nil || len(machines) != 0 {
		t.Fatalf("cluster deleted: %v %v", machines, err)
	}
	calls := f.Calls()
	if len(calls) == 0 || calls[0].Method != "CreateCluster" || calls[0].Spec.Name != "demo" {
		t.Fatalf("recorded calls: %+v", calls)
	}
}

func TestFailuresAndCancellation(t *testing.T) {
	f := fake.New()
	ctx := context.Background()
	want := errors.New("infrastructure failed")
	f.Errors["CreateCluster"] = want
	if err := f.CreateCluster(ctx, infra.ClusterSpec{Name: "demo"}); !errors.Is(err, want) {
		t.Fatalf("error: %v", err)
	}
	f.Delay = time.Second
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.AddMachine(canceled, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	machines, err := f.Machines(ctx, "demo")
	if err != nil || len(machines) != 0 {
		t.Fatalf("failed create changed state: %v %v", machines, err)
	}
}
