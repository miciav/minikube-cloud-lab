package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

func TestRegistryRoutes(t *testing.T) {
	_, s, f, url := setup(t)
	resp, data := call(t, "POST", url+"/v1/registries", `{"name":"corso"}`)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(data), `"CREATE_REGISTRY"`) || !strings.Contains(string(data), `"student"`) {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	operation(t, s, data)
	if resp, _ := call(t, "POST", url+"/v1/registries", `{"name":"x","bogus":1}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "POST", url+"/v1/registries", `{"name":"Bad_Name"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid name: %d", resp.StatusCode)
	}
	resp, data = call(t, "GET", url+"/v1/registries", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"corso.registry:5000"`) || strings.Contains(strings.ToLower(string(data)), "password") {
		t.Fatalf("list: %d %s", resp.StatusCode, data)
	}
	for access, user := range map[string]string{"push": `"push"`, "pull": `"pull"`} {
		resp, data := call(t, "GET", url+"/v1/registries/corso/credentials?access="+access, "")
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), user) || !strings.Contains(string(data), `"password"`) {
			t.Fatalf("%s credentials: %d %s", access, resp.StatusCode, data)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s credentials cached: %v", access, resp.Header)
		}
	}
	for _, query := range []string{"?access=admin", ""} {
		if resp, _ := call(t, "GET", url+"/v1/registries/corso/credentials"+query, ""); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("access %q: %d", query, resp.StatusCode)
		}
	}
	if resp, _ := call(t, "GET", url+"/v1/registries/missing/credentials?access=pull", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing credentials: %d", resp.StatusCode)
	}
	f.SetRegistryImages("corso", infra.RegistryImage{Repository: "web", Tag: "v1", Bytes: 42, Pushed: time.Unix(1, 0).UTC()})
	if resp, data := call(t, "GET", url+"/v1/registries/corso/images", ""); resp.StatusCode != http.StatusOK || !strings.Contains(string(data), `"web"`) {
		t.Fatalf("images: %d %s", resp.StatusCode, data)
	}
	if resp, _ := call(t, "DELETE", url+"/v1/registries/corso", ""); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("non-empty delete: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "DELETE", url+"/v1/registries/corso?force=maybe", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad force: %d", resp.StatusCode)
	}
	resp, data = call(t, "DELETE", url+"/v1/registries/corso?force=true", "")
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(data), `"DELETE_REGISTRY"`) {
		t.Fatalf("forced delete: %d %s", resp.StatusCode, data)
	}
	operation(t, s, data)
}

func TestRegistryClient(t *testing.T) {
	c, s, f, _ := setup(t)
	ctx := context.Background()
	op, err := c.CreateRegistry(ctx, "corso")
	if err != nil || op.Type != "CREATE_REGISTRY" {
		t.Fatalf("create: %+v %v", op, err)
	}
	wait(t, s, op)
	registries, err := c.Registries(ctx)
	if err != nil || len(registries) != 1 || registries[0].Status != "RUNNING" {
		t.Fatalf("list: %+v %v", registries, err)
	}
	push, err := c.RegistryCredentials(ctx, "corso", "push")
	if err != nil || push.Username != "push" || push.Password == "" || push.Endpoint != "corso.registry:5000" {
		t.Fatalf("push: %+v %v", push, err)
	}
	pull, err := c.RegistryCredentials(ctx, "corso", "pull")
	if err != nil || pull.Username != "pull" || pull.Password == push.Password {
		t.Fatalf("pull: %+v %v", pull, err)
	}
	_, err = c.RegistryCredentials(ctx, "corso", "admin")
	assertStatus(t, err, http.StatusBadRequest)
	f.SetRegistryImages("corso", infra.RegistryImage{Repository: "web", Tag: "v1", Bytes: 42})
	images, err := c.RegistryImages(ctx, "corso")
	if err != nil || len(images) != 1 || images[0].Tag != "v1" {
		t.Fatalf("images: %+v %v", images, err)
	}
	_, err = c.DeleteRegistry(ctx, "corso", false)
	assertStatus(t, err, http.StatusPreconditionFailed)
	op, err = c.DeleteRegistry(ctx, "corso", true)
	if err != nil || op.Type != "DELETE_REGISTRY" {
		t.Fatalf("delete: %+v %v", op, err)
	}
	wait(t, s, op)
	_, err = c.RegistryImages(ctx, "corso")
	assertStatus(t, err, http.StatusNotFound)
}
