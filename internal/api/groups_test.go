package api_test

import (
	"net/http"
	"strings"
	"testing"
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
