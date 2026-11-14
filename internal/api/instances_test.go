package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/api"
	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func call(t *testing.T, method, url, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var data json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&data)
	return resp, data
}

func operation(t *testing.T, s *cloud.Service, data []byte) {
	t.Helper()
	var op cloud.Operation
	if err := json.Unmarshal(data, &op); err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
}

func TestInstanceRoutes(t *testing.T) {
	_, s, _, url := setup(t)
	if resp, data := call(t, "GET", url+"/v1/images", ""); resp.StatusCode != http.StatusOK || !strings.Contains(string(data), "debian-12") {
		t.Fatalf("images: %d %s", resp.StatusCode, data)
	}
	resp, data := call(t, "POST", url+"/v1/instances", `{"name":"vm1","image":"debian-12","machineType":"lab-small"}`)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(data), `"CREATE_INSTANCE"`) {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	operation(t, s, data)
	if resp, _ := call(t, "POST", url+"/v1/instances", `{"name":"vm2","extra":1}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", resp.StatusCode)
	}
	if resp, data := call(t, "GET", url+"/v1/instances", ""); resp.StatusCode != http.StatusOK || strings.Count(string(data), `"name"`) != 1 {
		t.Fatalf("list: %d %s", resp.StatusCode, data)
	}
	if resp, _ := call(t, "GET", url+"/v1/instances/missing", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "GET", url+"/v1/instances/vm1/ssh", ""); resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("ssh: %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	resp, data = call(t, "POST", url+"/v1/instances/vm1/stop", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stop: %d %s", resp.StatusCode, data)
	}
	operation(t, s, data)
	if resp, _ := call(t, "GET", url+"/v1/instances/vm1/ssh", ""); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("ssh while stopped: %d", resp.StatusCode)
	}
	resp, data = call(t, "POST", url+"/v1/instances/vm1/start", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, data)
	}
	operation(t, s, data)
	resp, data = call(t, "DELETE", url+"/v1/instances/vm1", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete: %d %s", resp.StatusCode, data)
	}
	operation(t, s, data)
}

func TestInstanceClient(t *testing.T) {
	c, s, _, _ := setup(t)
	ctx := context.Background()
	if images, err := c.Images(ctx); err != nil || len(images) != 1 || images[0].Name != "debian-12" {
		t.Fatalf("images: %+v %v", images, err)
	}
	op, err := c.CreateInstance(ctx, "vm1", "debian-12", "lab-small")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	if items, err := c.Instances(ctx); err != nil || len(items) != 1 {
		t.Fatalf("list: %+v %v", items, err)
	}
	if item, err := c.Instance(ctx, "vm1"); err != nil || item.Status != "RUNNING" {
		t.Fatalf("get: %+v %v", item, err)
	}
	if access, err := c.InstanceSSH(ctx, "vm1"); err != nil || access.Port != 22 {
		t.Fatalf("ssh: %+v %v", access, err)
	}
	for _, step := range []func(context.Context, string) (cloud.Operation, error){c.StopInstance, c.StartInstance, c.DeleteInstance} {
		op, err := step(ctx, "vm1")
		if err != nil {
			t.Fatal(err)
		}
		wait(t, s, op)
	}
	var apiErr *api.Error
	if _, err := c.Instance(ctx, "missing"); !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("missing: %v", err)
	}
}
