package cloud_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
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

func TestGroupReconcilesAndRemovesOnlyOwnedInstances(t *testing.T) {
	s, _, _ := newService(t)
	createInstance(t, s, "unrelated")
	if err := s.ActivateInstanceAutoscaling(); err != nil {
		t.Fatal(err)
	}
	spec := cloud.GroupSpec{Name: "web", Image: "debian-12", MachineType: "lab-small", Bootstrap: "true", Min: 1, Desired: 2, Max: 3, Port: 8080, HealthPath: "/health", TargetRPS: 40}
	g, err := s.CreateGroup(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RunInstanceGroups(ctx)
	awaitGroup(t, func() bool {
		members := s.Instances()
		n := 0
		for _, i := range members {
			if i.GroupID == g.ID && i.Status == "RUNNING" {
				n++
			}
		}
		return n == 2
	})
	if err := s.DeleteGroup(g.ID); err != nil {
		t.Fatal(err)
	}
	awaitGroup(t, func() bool {
		members := s.Instances()
		return len(s.Groups()) == 0 && len(members) == 1 && members[0].Name == "unrelated"
	})
}

func awaitGroup(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("group did not converge")
}

func TestGroupRecoversInterruptedCreationByOwnership(t *testing.T) {
	for _, ownerMatches := range []bool{true, false} {
		name := "owned"
		if !ownerMatches {
			name = "foreign"
		}
		t.Run(name, func(t *testing.T) {
			s, f, dir := newService(t)
			if err := s.ActivateInstanceAutoscaling(); err != nil {
				t.Fatal(err)
			}
			g, err := s.CreateGroup(cloud.GroupSpec{Name: "web", Image: "debian-12", MachineType: "lab-small", Bootstrap: "true", Min: 1, Desired: 1, Max: 2, Port: 8080, HealthPath: "/health", TargetRPS: 40})
			if err != nil {
				t.Fatal(err)
			}
			owner := ""
			if ownerMatches {
				owner = g.ID
			}
			if _, err = f.CreateInstance(context.Background(), infra.InstanceSpec{Name: "interrupted", GroupID: owner}); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(dir, "state.json")
			data, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]any
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			state["instances"] = []cloud.Instance{{Name: "interrupted", GroupID: g.ID, Status: "CREATING"}}
			data, err = json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, data, 0600); err != nil {
				t.Fatal(err)
			}
			s, err = cloud.NewService(f, dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go s.RunInstanceGroups(ctx)
			awaitGroup(t, func() bool {
				group, _ := s.Group(g.ID)
				members := s.Instances()
				if ownerMatches {
					return len(members) == 1 && members[0].Name != "interrupted" && members[0].GroupID == g.ID && members[0].Status == "RUNNING"
				}
				return group.LastError != ""
			})
			observed, err := f.Instances(ctx)
			if err != nil || len(observed) != 1 || (observed[0].Name == "interrupted") == ownerMatches {
				t.Fatalf("runtime instances: %+v, %v", observed, err)
			}
		})
	}
}

func TestUninstallResumesAfterRestartAndRejectsLateScale(t *testing.T) {
	s, f, dir := newService(t)
	createInstance(t, s, "manual")
	if err := s.ActivateInstanceAutoscaling(); err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateGroup(cloud.GroupSpec{Name: "web", Image: "debian-12", MachineType: "lab-small",
		Bootstrap: "true", Min: 1, Desired: 1, Max: 2, Port: 8080, HealthPath: "/health", TargetRPS: 40})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.RunInstanceGroups(ctx) }()
	awaitGroup(t, func() bool {
		members := s.Instances()
		return len(members) == 2 && members[0].GroupID == group.ID && members[0].Status == "RUNNING"
	})
	cancel()
	<-done
	member := s.Instances()[0]
	stop, err := s.StopInstance(member.Name, "student")
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, stop); got.Status != "DONE" {
		t.Fatalf("stop member: %+v", got)
	}
	if err := s.BeginInstanceAutoscalingRemoval(); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyGroupPolicy(group.ID, 1, 2, "stale"); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("late scale: %v", err)
	}
	if err := s.FinishInstanceAutoscalingRemoval(); !errors.Is(err, cloud.ErrFailedPrecondition) {
		t.Fatalf("early completion: %v", err)
	}
	s, err = cloud.NewService(f, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go s.RunInstanceGroups(ctx)
	awaitGroup(t, func() bool { return len(s.InstanceAutoscaling().Remaining) == 0 })
	for range 2 {
		if err := s.FinishInstanceAutoscalingRemoval(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ActivateInstanceAutoscaling(); err != nil {
		t.Fatal(err)
	}
	instances := s.Instances()
	if len(s.Groups()) != 0 || len(instances) != 1 || instances[0].Name != "manual" {
		t.Fatalf("reinstallation state: groups=%+v instances=%+v", s.Groups(), instances)
	}
}

func TestGroupKeepsHealthyMemberWhenStartupInventoryFails(t *testing.T) {
	s, f, dir := newService(t)
	if err := s.ActivateInstanceAutoscaling(); err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateGroup(cloud.GroupSpec{Name: "web", Image: "debian-12", MachineType: "lab-small",
		Bootstrap: "true", Min: 1, Desired: 1, Max: 2, Port: 8080, HealthPath: "/health", TargetRPS: 40})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.RunInstanceGroups(ctx) }()
	awaitGroup(t, func() bool {
		members := s.Instances()
		return len(members) == 1 && members[0].GroupID == group.ID && members[0].Status == "RUNNING"
	})
	cancel()
	<-done
	member := s.Instances()[0]
	f.Errors["Instances"] = errors.New("runtime unavailable")
	restarted, err := cloud.NewService(f, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Instances(); len(got) != 1 || got[0] != member {
		t.Fatalf("healthy group member changed during inventory outage: %+v", got)
	}
}
