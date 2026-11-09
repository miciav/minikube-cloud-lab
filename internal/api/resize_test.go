package api_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResizeAPIValidation(t *testing.T) {
	c, s, _, base := setup(t)
	op, err := c.CreateCluster(t.Context(), "demo", "lab-small", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	cluster, _ := s.Cluster("demo")
	for _, tc := range []struct {
		body, tag string
		want      int
	}{
		{`{}`, "", 400},
		{`{"nodes":0}`, "", 400},
		{`{"nodes":3,"extra":1}`, "", 400},
		{`{"nodes":3} {}`, "", 400},
		{strings.Repeat(" ", 4096) + `{"nodes":3}`, "", 400},
		{`{"nodes":3}`, "invalid", 400},
		{`{"nodes":3}`, strconv.Quote(cluster.CreatedAt.Add(-time.Hour).Format(time.RFC3339Nano)), 412},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/v1/clusters/demo/resize", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		if tc.tag != "" {
			req.Header.Set("If-Match", tc.tag)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("body=%q tag=%q: status %d want %d", tc.body, tc.tag, res.StatusCode, tc.want)
		}
	}
	ops, err := c.ResizeCluster(t.Context(), "demo", 3)
	if err != nil || len(ops) != 2 {
		t.Fatal(ops, err)
	}
	for _, op := range ops {
		wait(t, s, op)
	}
	_, err = c.ResizeCluster(t.Context(), "demo", 1)
	assertStatus(t, err, http.StatusPreconditionFailed)
	_, err = c.ResizeCluster(t.Context(), "missing", 3)
	assertStatus(t, err, http.StatusNotFound)
}
