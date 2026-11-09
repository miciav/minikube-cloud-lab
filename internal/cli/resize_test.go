package cli

import (
	"strings"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func TestResizeClusterPreservesExistingCluster(t *testing.T) {
	f := fake.New()
	s, url := setup(t, f)
	if _, err := execute(t, url, "clusters", "create", "demo", "--max-nodes", "1"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Cluster("demo")
	for range 2 {
		out, err := execute(t, url, "clusters", "resize", "demo", "--nodes", "3")
		if err != nil || !strings.Contains(out, "Resized cluster demo to 3 nodes") {
			t.Fatalf("resize: %q %v", out, err)
		}
	}
	after, _ := s.Cluster("demo")
	machines, _ := s.Machines("demo")
	if len(machines) != 3 || after.MinMachines != 3 || after.MaxMachines != 3 || !before.CreatedAt.Equal(after.CreatedAt) || before.MachineType != after.MachineType {
		t.Fatalf("cluster identity/size: before=%+v after=%+v machines=%+v", before, after, machines)
	}
	creates, adds := 0, 0
	for _, call := range f.Calls() {
		switch call.Method {
		case "CreateCluster":
			creates++
		case "AddMachine":
			adds++
		case "DeleteCluster", "DeleteMachine":
			t.Fatalf("resize deleted existing infrastructure: %+v", call)
		}
	}
	if creates != 1 || adds != 2 {
		t.Fatalf("create=%d add=%d", creates, adds)
	}
	for _, args := range [][]string{{}, {"--nodes", "0"}, {"--nodes", "-1"}, {"--nodes", "2"}} {
		if _, err := execute(t, url, append([]string{"clusters", "resize", "demo"}, args...)...); err == nil {
			t.Fatalf("accepted invalid resize: %v", args)
		}
	}
}

func TestResizeClusterAsync(t *testing.T) {
	s, url := setup(t, nil)
	if _, err := execute(t, url, "clusters", "create", "demo", "--max-nodes", "1"); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, url, "clusters", "resize", "demo", "--nodes", "3", "--async")
	ids := strings.Fields(out)
	if err != nil || len(ids) != 2 {
		t.Fatalf("async resize: %q %v", out, err)
	}
	for _, id := range ids {
		op, err := s.Operation(id)
		if err != nil || op.Requester != "student" {
			t.Fatalf("operation: %+v %v", op, err)
		}
		wait(t, s, op)
	}
}
