// Package portal serves the local web UI and its fixed cloud API proxy.
package portal

import (
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/miciav/minikube-cloud-lab/internal/api"
)

// NewServer exposes assets and the cloud API without giving the browser infrastructure credentials.
func NewServer(assets fs.FS, upstream *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(upstream); r.Out.Host = r.In.Host }, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("{\"error\":\"cloud API unavailable\"}\n"))
	}}
	files := http.FileServer(http.FS(assets))
	return api.Management(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-src http://127.0.0.1:*; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
		if r.URL.IsAbs() || strings.ContainsAny(r.URL.EscapedPath(), "%\\") || path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "//") {
			http.Error(w, "invalid request path", http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Cache-Control", "no-store")
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		files.ServeHTTP(w, r)
	}))
}
