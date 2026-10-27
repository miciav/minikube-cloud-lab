package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/api"
)

func TestForeignHostCannotDownloadCredentials(t *testing.T) {
	c, s, _, _ := setup(t)
	op, err := c.CreateCluster(context.Background(), "demo", "lab-small", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	for _, tc := range []struct {
		host, origin, site string
		want               int
	}{
		{"attacker.example:8080", "http://attacker.example:8080", "same-origin", 403},
		{"attacker.example:8080", "", "", 403},
		{"127.0.0.1:8080", "http://attacker.example", "", 403},
		{"127.0.0.1:8080", "null", "", 403},
		{"127.0.0.1:8080", "", "cross-site", 403},
		{"127.0.0.1:8080", "", "", 200},
		{"localhost:8080", "http://localhost:8080", "same-origin", 200},
		{"[::1]:8080", "http://[::1]:8080", "same-origin", 200},
	} {
		t.Run(tc.host+tc.origin+tc.site, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+tc.host+"/v1/clusters/demo/kubeconfig", nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Sec-Fetch-Site", tc.site)
			recorder := httptest.NewRecorder()
			api.NewServer(s).ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("credentials status=%d want=%d (contents omitted)", recorder.Code, tc.want)
			}
		})
	}
}
