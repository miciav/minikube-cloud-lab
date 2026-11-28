package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestImageRoutesAndClient(t *testing.T) {
	c, s, _, url := setup(t)
	ctx := context.Background()
	_, data := call(t, "POST", url+"/v1/instances", `{"name":"vm1","image":"debian-12","machineType":"lab-small"}`)
	operation(t, s, data)
	_, data = call(t, "POST", url+"/v1/instances/vm1/stop", "")
	operation(t, s, data)
	resp, data := call(t, "POST", url+"/v1/images", `{"name":"web","from":"vm1","description":"d"}`)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(data), `"CREATE_IMAGE"`) {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	operation(t, s, data)
	if resp, data := call(t, "GET", url+"/v1/images/web", ""); resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"web"`) {
		t.Fatalf("get: %d %s", resp.StatusCode, data)
	}
	if resp, _ := call(t, "GET", url+"/v1/images/missing", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "POST", url+"/v1/images", `{"name":"x","bogus":1}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad json: %d", resp.StatusCode)
	}
	_, data = call(t, "POST", url+"/v1/instances", `{"name":"vm2","image":"web","machineType":"lab-small"}`)
	operation(t, s, data)
	if resp, _ := call(t, "DELETE", url+"/v1/images/web", ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("referenced: %d", resp.StatusCode)
	}
	_, data = call(t, "DELETE", url+"/v1/instances/vm2", "")
	operation(t, s, data)

	if _, err := c.CreateImage(ctx, "web", "vm1", ""); err == nil {
		t.Fatal("duplicate accepted")
	}
	img, err := c.Image(ctx, "web")
	if err != nil || img.Source != "vm1" {
		t.Fatalf("image: %+v %v", img, err)
	}
	op, err := c.DeleteImage(ctx, "web")
	if err != nil || op.Type != "DELETE_IMAGE" {
		t.Fatalf("delete: %+v %v", op, err)
	}
	wait(t, s, op)
	op, err = c.CreateImage(ctx, "web2", "vm1", "")
	if err != nil || op.Type != "CREATE_IMAGE" {
		t.Fatalf("client create: %+v %v", op, err)
	}
	wait(t, s, op)
	_, err = c.Image(ctx, "web")
	assertStatus(t, err, http.StatusNotFound)
}
