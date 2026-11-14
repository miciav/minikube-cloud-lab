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

func TestFakeInstanceLifecycle(t *testing.T) {
	f := fake.New()
	ctx := context.Background()
	spec := infra.InstanceSpec{Name: "vm1", Image: "debian-12", SSHUser: "docker", AuthorizedKey: "ssh-ed25519 AAA"}
	got, err := f.CreateInstance(ctx, spec)
	if err != nil || got != (infra.Instance{Name: "vm1", IP: "172.30.0.200", HostKey: "ssh-ed25519 FAKE-vm1", Running: true}) {
		t.Fatalf("create: %+v %v", got, err)
	}
	if second, err := f.CreateInstance(ctx, infra.InstanceSpec{Name: "vm2"}); err != nil || second.IP != "172.30.0.201" {
		t.Fatalf("second: %+v %v", second, err)
	}
	if err := f.StopInstance(ctx, "vm1"); err != nil {
		t.Fatal(err)
	}
	listed, err := f.Instances(ctx)
	if err != nil || len(listed) != 2 || listed[0].Name != "vm1" || listed[0].Running || listed[1].Name != "vm2" || !listed[1].Running {
		t.Fatalf("list: %+v %v", listed, err)
	}
	if started, err := f.StartInstance(ctx, "vm1"); err != nil || !started.Running || started.IP != "172.30.0.200" {
		t.Fatalf("start: %+v %v", started, err)
	}
	for range 2 {
		if err := f.DeleteInstance(ctx, "vm1"); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if _, err := f.StartInstance(ctx, "vm1"); err == nil {
		t.Fatal("started a deleted instance")
	}
	if calls := f.Calls(); calls[0].Method != "CreateInstance" || calls[0].Instance != spec {
		t.Fatalf("calls: %+v", calls[0])
	}
	boom := errors.New("boom")
	f.Errors["CreateInstance"] = boom
	if _, err := f.CreateInstance(ctx, infra.InstanceSpec{Name: "vm3"}); !errors.Is(err, boom) {
		t.Fatalf("configured error: %v", err)
	}
}
