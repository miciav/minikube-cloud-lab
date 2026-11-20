package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

type diskRequest struct {
	Size     string `json:"size,omitempty"`
	Instance string `json:"instance,omitempty"`
}

func diskRoutes(mux *http.ServeMux, svc *cloud.Service) {
	mux.HandleFunc("GET /v1/disks", func(w http.ResponseWriter, _ *http.Request) { d, e := svc.InstanceDisks(); respond(w, 200, d, e) })
	for _, op := range []struct {
		method, path, action string
		status               int
	}{
		{"POST", "/v1/disks/{name}", "create", 201},
		{"DELETE", "/v1/disks/{name}", "delete", 200},
		{"POST", "/v1/disks/{name}/attach", "attach", 200},
		{"POST", "/v1/disks/{name}/detach", "detach", 200},
	} {
		mux.HandleFunc(op.method+" "+op.path, func(w http.ResponseWriter, r *http.Request) {
			var req diskRequest
			if op.method != "DELETE" {
				if err := decodeJSON(w, r, &req); err != nil {
					respond(w, 400, nil, cloud.ErrInvalid)
					return
				}
			}
			d, e := svc.ChangeDisk(r.Context(), op.action, r.PathValue("name"), req.Size, req.Instance)
			respond(w, op.status, d, e)
		})
	}
}

// InstanceDisks returns the catalog of standalone instance data disks.
func (c *Client) InstanceDisks(ctx context.Context) ([]cloud.Disk, error) {
	var result []cloud.Disk
	_, err := c.request(ctx, http.MethodGet, "/v1/disks", nil, &result)
	return result, err
}

// ChangeDisk requests creation, attachment, detachment, or deletion of a disk.
func (c *Client) ChangeDisk(ctx context.Context, action, name, size, instance string) (cloud.Disk, error) {
	var result cloud.Disk
	method, path := http.MethodPost, "/v1/disks/"+url.PathEscape(name)
	var body any = diskRequest{Size: size, Instance: instance}
	switch action {
	case "create":
	case "delete":
		method = http.MethodDelete
		body = nil
	case "attach", "detach":
		path += "/" + action
	default:
		return result, fmt.Errorf("unknown disk action %q", action)
	}
	_, err := c.request(ctx, method, path, body, &result)
	return result, err
}
