// Package api exposes the cloud management API and its HTTP client.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

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
		case errors.Is(err, cloud.ErrUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, cloud.ErrInvalid):
			status = http.StatusBadRequest
		case errors.Is(err, cloud.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, cloud.ErrConflict), errors.Is(err, cloud.ErrStorageCapacity):
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

// parseSince reads a duration ("1h") or RFC3339 instant; empty means since the last reset.
func parseSince(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(value); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("%w: since must not be negative", cloud.ErrInvalid)
		}
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: since must be a duration or RFC3339 time", cloud.ErrInvalid)
	}
	return t, nil
}

// NewServer routes management requests to the shared cloud service.
func NewServer(svc *cloud.Service) http.Handler {
	mux := http.NewServeMux()
	registerGroups(mux, svc)
	registerMonitoring(mux, svc)
	diskRoutes(mux, svc)
	mux.HandleFunc("POST /v1/clusters/{name}/dashboard/{action}", func(w http.ResponseWriter, r *http.Request) {
		action := r.PathValue("action")
		if action != "install" && action != "uninstall" {
			http.NotFound(w, r)
			return
		}
		var req struct{ UID string }
		if err := decodeJSON(w, r, &req); err != nil {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
		op, err := svc.SetDashboard(r.Context(), r.PathValue("name"), req.UID, action == "install")
		respond(w, http.StatusAccepted, op, err)
	})
	mux.HandleFunc("GET /v1/clusters/{name}/dashboard", func(w http.ResponseWriter, r *http.Request) {
		v, err := svc.Dashboard(r.Context(), r.PathValue("name"))
		respond(w, 200, v, err)
	})
	mux.HandleFunc("GET /v1/clusters/{name}/monitoring", func(w http.ResponseWriter, r *http.Request) {
		v, err := svc.Monitoring(r.Context(), r.PathValue("name"))
		respond(w, 200, v, err)
	})
	mux.HandleFunc("GET /v1/billing", func(w http.ResponseWriter, r *http.Request) {
		since, err := parseSince(r.URL.Query().Get("since"), time.Now())
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		v, err := svc.Billing(since)
		respond(w, 200, v, err)
	})
	mux.HandleFunc("GET /v1/storage", func(w http.ResponseWriter, _ *http.Request) { v, err := svc.StorageCapacity(); respond(w, 200, v, err) })
	mux.HandleFunc("POST /v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Capacity string `json:"capacity"`
		}
		if err := decodeJSON(w, r, &request); err != nil {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
		v, err := svc.CreateAvailableVolume(request.Capacity) //nolint:contextcheck // durable allocation survives request cancellation.
		respond(w, 201, v, err)
	})
	mux.HandleFunc("POST /v1/volumes/{id}/publish", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Cluster string `json:"cluster"`
		}
		if err := decodeJSON(w, r, &request); err != nil {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
		if request.Cluster == "" {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
		expected, err := clusterGeneration(r)
		if err != nil {
			respond(w, 400, nil, err)
			return
		}
		respond(w, 200, map[string]string{"status": "published"}, svc.PublishVolumeIfUnchanged(r.Context(), r.PathValue("id"), request.Cluster, expected))
	})
	mux.HandleFunc("POST /v1/volumes/{id}/unpublish", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]string{"status": "unpublished"}, svc.UnpublishVolume(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("GET /v1/volumes", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, svc.Volumes(), nil) })
	mux.HandleFunc("DELETE /v1/volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]string{"status": "deleted"}, svc.DeleteVolume(r.PathValue("id"))) //nolint:contextcheck // durable deletion intent must survive request cancellation.
	})
	mux.HandleFunc("GET /v1/machine-types", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, cloud.MachineTypes(), nil) })
	mux.HandleFunc("GET /v1/clusters", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, svc.Clusters(), nil) })
	mux.HandleFunc("POST /v1/clusters", func(w http.ResponseWriter, r *http.Request) {
		var request createRequest
		if err := decodeJSON(w, r, &request); err != nil {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
		op, err := svc.CreateCluster(request.Name, request.MachineType, request.MinNodes, request.MaxNodes, "student") //nolint:contextcheck // accepted work must survive HTTP request cancellation.
		respond(w, http.StatusAccepted, op, err)
	})
	mux.HandleFunc("GET /v1/clusters/{name}", func(w http.ResponseWriter, r *http.Request) {
		c, err := svc.Cluster(r.PathValue("name"))
		if err == nil {
			w.Header().Set("ETag", strconv.Quote(c.CreatedAt.Format(time.RFC3339Nano)))
		}
		respond(w, 200, c, err)
	})
	mux.HandleFunc("DELETE /v1/clusters/{name}", func(w http.ResponseWriter, r *http.Request) {
		expected, err := clusterGeneration(r)
		if err != nil {
			respond(w, 400, nil, err)
			return
		}
		op, err := svc.DeleteClusterIfUnchanged(r.PathValue("name"), "student", expected) //nolint:contextcheck // accepted work must survive HTTP request cancellation.
		respond(w, http.StatusAccepted, op, err)
	})
	mux.HandleFunc("POST /v1/clusters/{name}/resize", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Nodes int `json:"nodes"`
		}
		if err := decodeJSON(w, r, &request); err != nil {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
		expected, err := clusterGeneration(r)
		if err != nil {
			respond(w, 400, nil, err)
			return
		}
		ops, err := svc.ResizeClusterIfUnchanged(r.PathValue("name"), request.Nodes, "student", expected) //nolint:contextcheck // accepted work survives HTTP request cancellation.
		respond(w, http.StatusAccepted, ops, err)
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
	mux.HandleFunc("GET /v1/images", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, cloud.Images(), nil) })
	mux.HandleFunc("GET /v1/instances", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, svc.Instances(), nil) })
	mux.HandleFunc("POST /v1/instances", func(w http.ResponseWriter, r *http.Request) {
		var request instanceRequest
		if err := decodeLimitedJSON(w, r, &request, 6*cloud.MaxUserDataBytes+4096); err != nil {
			respond(w, 400, nil, cloud.ErrInvalid)
			return
		}
		op, err := svc.CreateInstance(request.Name, request.Image, request.MachineType, "student", request.UserData) //nolint:contextcheck // accepted work must survive HTTP request cancellation.
		respond(w, http.StatusAccepted, op, err)
	})
	mux.HandleFunc("GET /v1/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		i, err := svc.Instance(r.PathValue("name"))
		respond(w, 200, i, err)
	})
	mux.HandleFunc("POST /v1/instances/{name}/start", func(w http.ResponseWriter, r *http.Request) {
		op, err := svc.StartInstance(r.PathValue("name"), "student") //nolint:contextcheck // accepted work must survive HTTP request cancellation.
		respond(w, http.StatusAccepted, op, err)
	})
	mux.HandleFunc("POST /v1/instances/{name}/stop", func(w http.ResponseWriter, r *http.Request) {
		op, err := svc.StopInstance(r.PathValue("name"), "student") //nolint:contextcheck // accepted work must survive HTTP request cancellation.
		respond(w, http.StatusAccepted, op, err)
	})
	mux.HandleFunc("DELETE /v1/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		op, err := svc.DeleteInstance(r.PathValue("name"), "student") //nolint:contextcheck // accepted work must survive HTTP request cancellation.
		respond(w, http.StatusAccepted, op, err)
	})
	mux.HandleFunc("GET /v1/instances/{name}/ssh", func(w http.ResponseWriter, r *http.Request) {
		access, err := svc.InstanceSSH(r.PathValue("name"))
		w.Header().Set("Cache-Control", "no-store")
		respond(w, 200, access, err)
	})
	mux.HandleFunc("GET /v1/operations", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, svc.Operations(), nil) })
	mux.HandleFunc("GET /v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		op, err := svc.Operation(r.PathValue("id"))
		respond(w, 200, op, err)
	})
	return Management(mux)
}

// decodeJSON accepts exactly one small JSON object without unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	return decodeLimitedJSON(w, r, value, 4096)
}

func decodeLimitedJSON(w http.ResponseWriter, r *http.Request, value any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return cloud.ErrInvalid
	}
	return nil
}
