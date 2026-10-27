package cloud_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

type partialCreation struct {
	*fake.Infra
	hideResult bool
}

func (f partialCreation) AddMachine(ctx context.Context, name string) (infra.Machine, error) {
	m, err := f.Infra.AddMachine(ctx, name)
	if err != nil {
		return m, err
	}
	if f.hideResult {
		m = infra.Machine{}
	}
	return m, errors.New("resource update failed after creation")
}

func TestPartialAddRemainsTracked(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprint(hidden), func(t *testing.T) {
			f := partialCreation{fake.New(), hidden}
			s, err := cloud.NewService(f, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			op, err := s.CreateCluster("demo", "lab-small", 1, 2, "student")
			if err != nil {
				t.Fatal(err)
			}
			wait(t, s, op)
			ops, err := s.AddMachines("demo", 1, "cluster-autoscaler")
			if err != nil {
				t.Fatal(err)
			}
			wait(t, s, ops[0])
			observed, err := f.Machines(context.Background(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			recorded, _ := s.Machines("demo")
			if len(recorded) != len(observed) {
				t.Fatalf("actual=%d recorded=%d: failed addition is no longer owned", len(observed), len(recorded))
			}
			if recorded[1].CreatedBy != "cluster-autoscaler" {
				t.Fatal("partial addition lost requester")
			}
			if _, err := s.AddMachines("demo", 1, "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
				t.Fatalf("partial machine did not reserve maximum: %v", err)
			}
		})
	}
}

func TestFailedObservationKeepsAddReservation(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprint(hidden), func(t *testing.T) {
			f := partialCreation{fake.New(), hidden}
			s, err := cloud.NewService(f, t.TempDir(), slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			op, err := s.CreateCluster("demo", "lab-small", 1, 2, "student")
			if err != nil {
				t.Fatal(err)
			}
			wait(t, s, op)
			f.Errors["Machines"] = errors.New("cannot observe Docker")
			ops, err := s.AddMachines("demo", 1, "student")
			if err != nil {
				t.Fatal(err)
			}
			wait(t, s, ops[0])
			machines, _ := s.Machines("demo")
			if !hidden && machines[1].Name != "demo-m02" {
				t.Fatalf("known partial identity discarded: %s", machines[1].Name)
			}
			if _, err := s.AddMachines("demo", 1, "student"); !errors.Is(err, cloud.ErrFailedPrecondition) {
				t.Fatalf("uncertain capacity was released: %v", err)
			}
		})
	}
}

func TestRestartWithUnobservableMachinesStillAllowsCleanup(t *testing.T) {
	s, f, dir := newService(t)
	create(t, s, 1)
	f.Errors["Machines"] = errors.New("Docker temporarily unavailable")
	restarted, err := cloud.NewService(f, dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	machines, err := restarted.Machines("demo")
	if err != nil || len(machines) != 1 {
		t.Fatalf("ownership lost: %v %v", machines, err)
	}
	c, err := restarted.Cluster("demo")
	if err != nil || c.Status != "ERROR" {
		t.Fatalf("unverified cluster looks healthy: %v %v", c, err)
	}
	restarted, err = cloud.NewService(f, dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	op, err := restarted.DeleteCluster("demo", "student")
	if err != nil {
		t.Fatal(err)
	}
	if result := wait(t, restarted, op); result.Status != "DONE" {
		t.Fatalf("cleanup: %v", result)
	}
}
