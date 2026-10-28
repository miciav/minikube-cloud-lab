package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Management rejects browser requests outside the local, same-origin management surface.
func Management(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		trusted := strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			trusted = trusted && err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
		}
		if !trusted || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			respond(w, http.StatusForbidden, map[string]string{"error": "untrusted management request"}, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
