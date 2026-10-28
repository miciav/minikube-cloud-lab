package api_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func TestClusterMutationGenerationPrecondition(t *testing.T) {
	_, s, _, base := setup(t)
	op, err := s.CreateCluster("demo", "lab-small", 1, 2, "student")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	c, _ := s.Cluster("demo")
	for _, tc := range []struct {
		method, path, body, tag string
		want                    int
	}{
		{"DELETE", "clusters/demo", "", strconv.Quote(c.CreatedAt.Add(-time.Hour).Format(time.RFC3339Nano)), 412},
		{"POST", "volumes/missing/publish", `{"cluster":"demo"}`, strconv.Quote(c.CreatedAt.Add(-time.Hour).Format(time.RFC3339Nano)), 412},
		{"DELETE", "clusters/demo", "", "invalid", 400},
		{"DELETE", "clusters/demo", "", `W/"weak"`, 400},
		{"DELETE", "clusters/demo", "", strconv.Quote("invalid-date"), 400},
		{"POST", "volumes/missing/publish", `{"cluster":"demo"}`, strconv.Quote(c.CreatedAt.Format(time.RFC3339Nano)), 404},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, base+"/v1/"+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("If-Match", tc.tag)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode != tc.want {
			t.Fatalf("%s %s: got%d want%d", tc.method, tc.path, r.StatusCode, tc.want)
		}
	}
	r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/clusters/demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.Header.Get("ETag") != strconv.Quote(c.CreatedAt.Format(time.RFC3339Nano)) {
		t.Fatal("missing cluster generation ETag")
	}
	_ = res.Body.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, base+"/v1/clusters/demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("If-Match", strconv.Quote(c.CreatedAt.Format(time.RFC3339Nano)))
	req.Header.Add("If-Match", strconv.Quote("other"))
	duplicate, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = duplicate.Body.Close()
	if duplicate.StatusCode != http.StatusBadRequest {
		t.Fatal("ambiguous generation accepted")
	}
	req, err = http.NewRequestWithContext(t.Context(), http.MethodDelete, base+"/v1/clusters/demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-Match", strconv.Quote(c.CreatedAt.Format(time.RFC3339Nano)))
	accepted, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = accepted.Body.Close() }()
	if accepted.StatusCode != http.StatusAccepted {
		t.Fatal("matching generation rejected", accepted.StatusCode)
	}
	var deletion cloud.Operation
	if err := json.NewDecoder(accepted.Body).Decode(&deletion); err != nil {
		t.Fatal(err)
	}
	wait(t, s, deletion)
}
