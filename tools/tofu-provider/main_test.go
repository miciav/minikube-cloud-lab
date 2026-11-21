package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/miciav/minikube-cloud-lab/internal/api"
)

func TestInstanceCloudInitAndDrift(t *testing.T) {
	p := provider()
	if err := p.InternalValidate(); err != nil {
		t.Fatal(err)
	}
	r := p.ResourcesMap["lab_instance"]
	config := "#cloud-config\nusers: [default]\n"
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]any{"name": "api", "user_data": config})
	exists := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == "POST" && req.URL.Path == "/v1/instances":
			var body map[string]string
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body["userData"] != config {
				t.Errorf("cloud-init payload mismatch: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"op-1","status":"DONE"}`))
		case req.Method == "GET" && req.URL.Path == "/v1/instances/api" && exists:
			_, _ = w.Write([]byte(`{"name":"api","machineType":"lab-small","image":"ubuntu-26.04","ip":"10.0.0.5"}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	c := api.NewClient(server.URL)
	if ds := r.CreateContext(t.Context(), d, c); ds.HasError() {
		t.Fatal(ds)
	}
	if d.Id() != "api" || d.Get("ip") != "10.0.0.5" {
		t.Fatal("instance state was not refreshed")
	}
	exists = false
	if ds := r.ReadContext(t.Context(), d, c); ds.HasError() || d.Id() != "" {
		t.Fatalf("missing instance: %v, id %q", ds, d.Id())
	}
}
