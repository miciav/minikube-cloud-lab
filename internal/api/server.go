// Package api exposes the cloud management API and its HTTP client.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

type createRequest struct {
	Name        string `json:"name"`
	MachineType string `json:"machineType"`
	MinNodes    int    `json:"minNodes"`
	MaxNodes    int    `json:"maxNodes"`
}

func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		status = http.StatusInternalServerError
		switch {
		case errors.Is(err, cloud.ErrInvalid):
			status = http.StatusBadRequest
		case errors.Is(err, cloud.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, cloud.ErrConflict):
			status = http.StatusConflict
		case errors.Is(err, cloud.ErrFailedPrecondition):
			status = http.StatusPreconditionFailed
		}
		value = map[string]string{"error": err.Error()}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// NewServer routes management requests to the shared cloud service.
func NewServer(svc *cloud.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/volumes", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, svc.Volumes(), nil) })
	mux.HandleFunc("DELETE /v1/volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]string{"status": "deleted"}, svc.DeleteVolume(r.PathValue("id")))
	})
	mux.HandleFunc("GET /v1/machine-types", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, cloud.MachineTypes(), nil) })
	mux.HandleFunc("GET /v1/clusters", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, svc.Clusters(), nil) })
	mux.HandleFunc("POST /v1/clusters", func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		var request createRequest
		if err := decoder.Decode(&request); err != nil {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
		op, err := svc.CreateCluster(request.Name, request.MachineType, request.MinNodes, request.MaxNodes, "student") //nolint:contextcheck // accepted work must survive HTTP request cancellation.
		respond(w, http.StatusAccepted, op, err)
	})
	mux.HandleFunc("GET /v1/clusters/{name}", func(w http.ResponseWriter, r *http.Request) {
		c, err := svc.Cluster(r.PathValue("name"))
		respond(w, 200, c, err)
	})
	mux.HandleFunc("DELETE /v1/clusters/{name}", func(w http.ResponseWriter, r *http.Request) {
		op, err := svc.DeleteCluster(r.PathValue("name"), "student") //nolint:contextcheck // accepted work must survive HTTP request cancellation.
		respond(w, http.StatusAccepted, op, err)
	})
	mux.HandleFunc("GET /v1/clusters/{name}/kubeconfig", func(w http.ResponseWriter, r *http.Request) {
		data, err := svc.Kubeconfig(r.PathValue("name")) //nolint:contextcheck // the service owns infrastructure contexts.
		if err != nil {
			respond(w, 500, nil, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data) // #nosec G705 -- kubeconfig is served as application/yaml with nosniff, never HTML.
	})
	mux.HandleFunc("GET /v1/machines", func(w http.ResponseWriter, r *http.Request) {
		machines, err := svc.Machines(r.URL.Query().Get("cluster"))
		respond(w, 200, machines, err)
	})
	mux.HandleFunc("GET /v1/operations", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, svc.Operations(), nil) })
	mux.HandleFunc("GET /v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		op, err := svc.Operation(r.PathValue("id"))
		respond(w, 200, op, err)
	})
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
		mux.ServeHTTP(w, r)
	})
}
