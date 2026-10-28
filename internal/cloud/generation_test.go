package cloud

import (
	"errors"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func TestDeleteClusterRejectsReplacedGenerationAtomically(t *testing.T) {
	s, err := NewService(fake.New(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.CreateCluster("demo", "lab-small", 1, 2, "student")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Wait(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	current, err := s.Cluster("demo")
	if err != nil {
		t.Fatal(err)
	}
	old := current.CreatedAt.Add(-time.Hour)
	if _, err = s.DeleteClusterIfUnchanged("demo", "browser", &old); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("stale delete accepted: %v", err)
	}
	after, _ := s.Cluster("demo")
	if after.Status != "RUNNING" || len(s.Operations()) != 1 {
		t.Fatal("stale request changed state")
	}
	op, err = s.DeleteClusterIfUnchanged("demo", "browser", &current.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Wait(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPublishVolumeRejectsReplacedClusterGeneration(t *testing.T) {
	s, v := staticService(t)
	now := time.Now().UTC()
	s.state.Clusters[0].CreatedAt = now
	old := now.Add(-time.Hour)
	if err := s.PublishVolumeIfUnchanged(t.Context(), v.ID, "demo", &old); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatal(err)
	}
	if s.Volumes()[0].Status != "Available" || s.Volumes()[0].Cluster != "" {
		t.Fatal("stale publication reserved volume")
	}
	if err := s.reservePublication(v.ID, "demo", &now); err != nil {
		t.Fatal(err)
	}
}
