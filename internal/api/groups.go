package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

type resizeGroupRequest struct {
	Desired int    `json:"desired"`
	Reason  string `json:"reason,omitempty"`
}

func registerGroups(mux *http.ServeMux, svc *cloud.Service) {
	mux.HandleFunc("GET /v1/groups", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, svc.Groups(), nil)
	})
	mux.HandleFunc("POST /v1/groups", func(w http.ResponseWriter, r *http.Request) {
		var spec cloud.GroupSpec
		if err := decodeJSONLimit(w, r, &spec, 128*1024); err != nil {
			respond(w, http.StatusBadRequest, nil, cloud.ErrInvalid)
			return
		}
		group, err := svc.CreateGroup(spec)
		respond(w, http.StatusCreated, group, err)
	})
	mux.HandleFunc("GET /v1/groups/{key}", func(w http.ResponseWriter, r *http.Request) {
		group, err := svc.Group(r.PathValue("key"))
		respond(w, http.StatusOK, group, err)
	})
	mux.HandleFunc("POST /v1/groups/{key}/resize", func(w http.ResponseWriter, r *http.Request) {
		var request resizeGroupRequest
		if err := decodeJSON(w, r, &request); err != nil {
			respond(w, http.StatusBadRequest, nil, cloud.ErrInvalid)
			return
		}
		group, err := svc.ResizeGroup(r.PathValue("key"), request.Desired, "student", request.Reason)
		respond(w, http.StatusOK, group, err)
	})
	mux.HandleFunc("DELETE /v1/groups/{key}", func(w http.ResponseWriter, r *http.Request) {
		err := svc.DeleteGroup(r.PathValue("key"))
		respond(w, http.StatusAccepted, map[string]string{"status": "deleting"}, err)
	})
}

// Groups lists instance groups.
func (c *Client) Groups(ctx context.Context) ([]cloud.Group, error) {
	var result []cloud.Group
	_, err := c.request(ctx, http.MethodGet, "/v1/groups", nil, &result)
	return result, err
}

// CreateGroup creates a group with its launch template.
func (c *Client) CreateGroup(ctx context.Context, spec cloud.GroupSpec) (cloud.Group, error) {
	var result cloud.Group
	_, err := c.request(ctx, http.MethodPost, "/v1/groups", spec, &result)
	return result, err
}

// Group describes a group.
func (c *Client) Group(ctx context.Context, key string) (cloud.Group, error) {
	var result cloud.Group
	_, err := c.request(ctx, http.MethodGet, "/v1/groups/"+url.PathEscape(key), nil, &result)
	return result, err
}

// ResizeGroup sets the group's desired capacity.
func (c *Client) ResizeGroup(ctx context.Context, key string, desired int) (cloud.Group, error) {
	var result cloud.Group
	_, err := c.request(ctx, http.MethodPost, "/v1/groups/"+url.PathEscape(key)+"/resize", resizeGroupRequest{Desired: desired}, &result)
	return result, err
}

// DeleteGroup requests removal of a group and its instances.
func (c *Client) DeleteGroup(ctx context.Context, key string) error {
	_, err := c.request(ctx, http.MethodDelete, "/v1/groups/"+url.PathEscape(key), nil, nil)
	return err
}
