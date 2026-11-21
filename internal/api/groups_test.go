package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func TestInstanceGroupRoutes(t *testing.T) {
	_, s, _, base := setup(t)
	root := base + "/v1/groups"
	body := `{"name":"web","image":"debian-12","machineType":"lab-small","bootstrap":"true","min":1,"desired":2,"max":3,"port":8080,"healthPath":"/health","targetRPS":40}`
	if resp, _ := call(t, http.MethodPost, root, body); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("module off: %d", resp.StatusCode)
	}
	if err := s.ActivateInstanceAutoscaling(); err != nil {
		t.Fatal(err)
	}
	if resp, data := call(t, http.MethodPost, root, body); resp.StatusCode != http.StatusCreated || !strings.Contains(string(data), `"templateVersion":1`) {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	if resp, data := call(t, http.MethodPost, root+"/web/resize", `{"desired":3}`); resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"desired":3`) {
		t.Fatalf("resize: %d %s", resp.StatusCode, data)
	}
	if resp, data := call(t, http.MethodGet, root, ""); resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"name":"web"`) {
		t.Fatalf("list: %d %s", resp.StatusCode, data)
	}
}

func TestInstanceGroupClientWorkflow(t *testing.T) {
	client, service, _, base := setup(t)
	ctx := t.Context()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()
	service.ConfigureInstanceGroupRouting(stub.URL, stub.URL, stub.URL)
	for _, action := range []string{"", "prepare", "activate"} {
		if _, err := client.InstanceAutoscaling(ctx, action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	spec := cloud.GroupSpec{Name: "web", Image: "debian-12", MachineType: "lab-small", Bootstrap: "true", Min: 1, Desired: 2, Max: 3, Port: 8080, HealthPath: "/health", TargetRPS: 40}
	group, err := client.CreateGroup(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := client.Groups(ctx); err != nil || len(list) != 1 || list[0].ID != group.ID {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if got, err := client.Group(ctx, group.ID); err != nil || got.Name != "web" {
		t.Fatalf("describe: %+v, %v", got, err)
	}
	if got, err := client.ResizeGroup(ctx, "web", 3); err != nil || got.Desired != 3 {
		t.Fatalf("resize: %+v, %v", got, err)
	}
	if resp, _ := call(t, "POST", base+"/v1/groups/"+group.ID+"/scale", `{"expected":2,"desired":2}`); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("scale using a stale count: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "POST", base+"/v1/groups/"+group.ID+"/scale", `{"expected":3,"desired":3}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("duplicate scale: %d", resp.StatusCode)
	}
	if err := client.DeleteGroup(ctx, group.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := client.InstanceAutoscaling(ctx, "begin-removal"); err != nil || state.State != "removing" || len(state.Remaining) != 1 {
		t.Fatalf("begin removal: %+v, %v", state, err)
	}
	if _, err := client.InstanceAutoscaling(ctx, "finish-removal"); err == nil {
		t.Fatal("finished removal despite a pending group")
	}
}

func TestMalformedGroupRequests(t *testing.T) {
	_, _, _, base := setup(t)
	for _, suffix := range []string{"/groups", "/groups/web/resize", "/groups/asg-id/scale"} {
		if resp, _ := call(t, "POST", base+"/v1"+suffix, `{"unexpected":[]}`); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d", suffix, resp.StatusCode)
		}
	}
	if resp, _ := call(t, "POST", base+"/v1/instance-autoscaling/unknown", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown module action: %d", resp.StatusCode)
	}
}
