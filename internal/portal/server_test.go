package portal

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/miciav/minikube-cloud-lab/internal/api"
	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func portalAssets() fstest.MapFS {
	return fstest.MapFS{"index.html": {Data: []byte("<html>Portal</html>")}, "assets/main.js": {Data: []byte("console.log('portal')")}}
}

func TestPortalRejectsUntrustedRequests(t *testing.T) {
	called := false
	up := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	h := NewServer(portalAssets(), u)
	for _, c := range []struct{ target, host, origin, site string }{{"/v1/clusters", "evil.test", "", ""}, {"/v1/clusters", "localhost:3000", "http://evil.test", ""}, {"/v1/clusters", "localhost:3000", "", "cross-site"}, {"http://evil.test/v1/clusters", "localhost:3000", "", ""}, {"/v1/clusters%2fdemo", "localhost:3000", "", ""}, {"/v1/../secret", "localhost:3000", "", ""}, {"//evil.test/v1/clusters", "localhost:3000", "", ""}} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, c.target, nil)
		r.Host = c.host
		r.Header.Set("Origin", c.origin)
		r.Header.Set("Sec-Fetch-Site", c.site)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 400 || called {
			t.Fatalf("untrusted request reached upstream: %+v %d", c, w.Code)
		}
	}
}

func TestPortalProxyPreservesManagementChecks(t *testing.T) {
	s, err := cloud.NewService(fake.New(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(api.NewServer(s))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	h := NewServer(portalAssets(), u)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/machine-types", nil)
	r.Host = "localhost:3000"
	r.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing frame policy")
	}
}

func TestPortalStaticAndUpstreamFailure(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:1")
	h := NewServer(portalAssets(), u)
	for _, tc := range []struct {
		path   string
		status int
	}{{"/", 200}, {"/assets/main.js", 200}, {"/missing.js", 404}, {"/v1/clusters", 502}} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
		r.Host = "localhost:3000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %d %s", tc.path, w.Code, w.Body)
		}
		if tc.status == 502 {
			b, _ := io.ReadAll(w.Result().Body)
			if string(b) != "{\"error\":\"cloud API unavailable\"}\n" {
				t.Fatalf("leaked upstream details %s", b)
			}
		}
	}
}
