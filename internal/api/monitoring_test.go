package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestMonitoringRoutesReturnDomainErrors(t *testing.T) {
	_, s, _, base := setup(t)
	for _, name := range []string{"missing", "demo"} {
		if name == "demo" {
			op, err := s.CreateCluster("demo", "lab-small", 1, 2, "student")
			if err != nil {
				t.Fatal(err)
			}
			wait(t, s, op)
		}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/clusters/"+name+"/monitoring", nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		want := 404
		if name == "demo" {
			want = 503
		}
		var e map[string]string
		if r.StatusCode != want || json.Unmarshal(body, &e) != nil || e["error"] == "" {
			t.Fatalf("%s: status=%d body=%s", name, r.StatusCode, body)
		}
	}
}
