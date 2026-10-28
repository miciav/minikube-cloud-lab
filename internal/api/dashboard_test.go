package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestDashboardRouteMapsLifecycleErrors(t *testing.T) {
	_, s, _, base := setup(t)
	for _, name := range []string{"missing", "demo"} {
		if name == "demo" {
			op, err := s.CreateCluster("demo", "lab-small", 1, 2, "student")
			if err != nil {
				t.Fatal(err)
			}
			wait(t, s, op)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/clusters/"+name+"/dashboard", nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = r.Body.Close()
		want := 404
		if name == "demo" {
			want = 503
		}
		if r.StatusCode != want || body["error"] == "" {
			t.Fatalf("%d %+v", r.StatusCode, body)
		}
	}
}
