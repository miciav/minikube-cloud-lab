package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

// Error preserves the server's status and human-readable failure.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Client is the management API client used by the student CLI.
type Client struct {
	base string
	http *http.Client
}

// NewClient constructs an HTTP client for the configured API endpoint.
func NewClient(baseURL string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) request(ctx context.Context, method, path string, input, output any) ([]byte, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &failure); err != nil || failure.Error == "" {
			failure.Error = fmt.Sprintf("cloud API: %s", resp.Status)
		}
		return nil, &Error{Status: resp.StatusCode, Message: failure.Error}
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			return nil, fmt.Errorf("decode cloud API response: %w", err)
		}
	}
	return data, nil
}

// MachineTypes lists the catalog.
func (c *Client) MachineTypes(ctx context.Context) ([]cloud.MachineType, error) {
	var result []cloud.MachineType
	_, err := c.request(ctx, http.MethodGet, "/v1/machine-types", nil, &result)
	return result, err
}

// CreateCluster requests a cluster asynchronously.
func (c *Client) CreateCluster(ctx context.Context, name, kind string, minM, maxM int) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodPost, "/v1/clusters", createRequest{Name: name, MachineType: kind, MinNodes: minM, MaxNodes: maxM}, &result)
	return result, err
}

// ResizeCluster requests additive growth with min=max=nodes.
func (c *Client) ResizeCluster(ctx context.Context, name string, nodes int) ([]cloud.Operation, error) {
	var result []cloud.Operation
	_, err := c.request(ctx, http.MethodPost, "/v1/clusters/"+url.PathEscape(name)+"/resize", map[string]int{"nodes": nodes}, &result)
	return result, err
}

// Clusters lists clusters.
func (c *Client) Clusters(ctx context.Context) ([]cloud.Cluster, error) {
	var result []cloud.Cluster
	_, err := c.request(ctx, http.MethodGet, "/v1/clusters", nil, &result)
	return result, err
}

// Cluster describes a cluster.
func (c *Client) Cluster(ctx context.Context, name string) (cloud.Cluster, error) {
	var result cloud.Cluster
	_, err := c.request(ctx, http.MethodGet, "/v1/clusters/"+url.PathEscape(name), nil, &result)
	return result, err
}

// DeleteCluster requests teardown asynchronously.
func (c *Client) DeleteCluster(ctx context.Context, name string) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodDelete, "/v1/clusters/"+url.PathEscape(name), nil, &result)
	return result, err
}

// Kubeconfig downloads credentials with embedded certificates.
func (c *Client) Kubeconfig(ctx context.Context, name string) ([]byte, error) {
	return c.request(ctx, http.MethodGet, "/v1/clusters/"+url.PathEscape(name)+"/kubeconfig", nil, nil)
}

// Machines lists machines, optionally filtered by cluster.
func (c *Client) Machines(ctx context.Context, name string) ([]cloud.Machine, error) {
	var result []cloud.Machine
	_, err := c.request(ctx, http.MethodGet, "/v1/machines?cluster="+url.QueryEscape(name), nil, &result)
	return result, err
}

// Operations lists requests newest first.
func (c *Client) Operations(ctx context.Context) ([]cloud.Operation, error) {
	var result []cloud.Operation
	_, err := c.request(ctx, http.MethodGet, "/v1/operations", nil, &result)
	return result, err
}

// Operation describes a single request.
func (c *Client) Operation(ctx context.Context, id string) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodGet, "/v1/operations/"+url.PathEscape(id), nil, &result)
	return result, err
}

// Volumes lists cloud storage allocations.
func (c *Client) Volumes(ctx context.Context) ([]cloud.Volume, error) {
	var result []cloud.Volume
	_, err := c.request(ctx, http.MethodGet, "/v1/volumes", nil, &result)
	return result, err
}

// DeleteVolume removes an available volume.
func (c *Client) DeleteVolume(ctx context.Context, id string) error {
	_, err := c.request(ctx, http.MethodDelete, "/v1/volumes/"+url.PathEscape(id), nil, nil)
	return err
}

// Storage reports the global storage allocation budget.
func (c *Client) Storage(ctx context.Context) (cloud.StorageCapacity, error) {
	var v cloud.StorageCapacity
	_, err := c.request(ctx, http.MethodGet, "/v1/storage", nil, &v)
	return v, err
}

// CreateVolume allocates unclaimed persistent storage.
func (c *Client) CreateVolume(ctx context.Context, capacity string) (cloud.Volume, error) {
	var v cloud.Volume
	_, err := c.request(ctx, http.MethodPost, "/v1/volumes", map[string]string{"capacity": capacity}, &v)
	return v, err
}

// PublishVolume creates an unbound static PV in a managed cluster.
func (c *Client) PublishVolume(ctx context.Context, id, cluster string) error {
	_, err := c.request(ctx, http.MethodPost, "/v1/volumes/"+url.PathEscape(id)+"/publish", map[string]string{"cluster": cluster}, nil)
	return err
}

// UnpublishVolume removes an unused PV while retaining its cloud volume.
func (c *Client) UnpublishVolume(ctx context.Context, id string) error {
	_, err := c.request(ctx, http.MethodPost, "/v1/volumes/"+url.PathEscape(id)+"/unpublish", nil, nil)
	return err
}

type instanceRequest struct {
	UserData    string `json:"userData,omitempty"`
	Name        string `json:"name"`
	Image       string `json:"image"`
	MachineType string `json:"machineType"`
}

type imageRequest struct {
	Name        string `json:"name"`
	From        string `json:"from"`
	Description string `json:"description,omitempty"`
}

func imagePath(name string) string { return "/v1/images/" + url.PathEscape(name) }

func instancePath(name string) string { return "/v1/instances/" + url.PathEscape(name) }

// Images lists the instance image catalog.
func (c *Client) Images(ctx context.Context) ([]cloud.Image, error) {
	var result []cloud.Image
	_, err := c.request(ctx, http.MethodGet, "/v1/images", nil, &result)
	return result, err
}

// CreateImage captures a stopped instance asynchronously.
func (c *Client) CreateImage(ctx context.Context, name, from, description string) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodPost, "/v1/images", imageRequest{Name: name, From: from, Description: description}, &result)
	return result, err
}

// Image describes one catalog image.
func (c *Client) Image(ctx context.Context, name string) (cloud.Image, error) {
	var result cloud.Image
	_, err := c.request(ctx, http.MethodGet, imagePath(name), nil, &result)
	return result, err
}

// DeleteImage requests removal of a personal image asynchronously.
func (c *Client) DeleteImage(ctx context.Context, name string) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodDelete, imagePath(name), nil, &result)
	return result, err
}

// CreateInstance requests a standalone instance asynchronously.
func (c *Client) CreateInstance(ctx context.Context, name, image, kind, userData string) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodPost, "/v1/instances", instanceRequest{UserData: userData, Name: name, Image: image, MachineType: kind}, &result)
	return result, err
}

// Instances lists standalone instances.
func (c *Client) Instances(ctx context.Context) ([]cloud.Instance, error) {
	var result []cloud.Instance
	_, err := c.request(ctx, http.MethodGet, "/v1/instances", nil, &result)
	return result, err
}

// Instance describes a standalone instance.
func (c *Client) Instance(ctx context.Context, name string) (cloud.Instance, error) {
	var result cloud.Instance
	_, err := c.request(ctx, http.MethodGet, instancePath(name), nil, &result)
	return result, err
}

// StartInstance requests a boot asynchronously.
func (c *Client) StartInstance(ctx context.Context, name string) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodPost, instancePath(name)+"/start", nil, &result)
	return result, err
}

// StopInstance requests a halt asynchronously.
func (c *Client) StopInstance(ctx context.Context, name string) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodPost, instancePath(name)+"/stop", nil, &result)
	return result, err
}

// DeleteInstance requests removal asynchronously.
func (c *Client) DeleteInstance(ctx context.Context, name string) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodDelete, instancePath(name), nil, &result)
	return result, err
}

// InstanceSSH downloads the SSH credentials of a running instance.
func (c *Client) InstanceSSH(ctx context.Context, name string) (cloud.SSHAccess, error) {
	var result cloud.SSHAccess
	_, err := c.request(ctx, http.MethodGet, instancePath(name)+"/ssh", nil, &result)
	return result, err
}

// SetDashboard requests an addon operation for the selected Kubernetes cluster identity.
func (c *Client) SetDashboard(ctx context.Context, name, uid, action string) (cloud.Operation, error) {
	var op cloud.Operation
	_, err := c.request(ctx, http.MethodPost, "/v1/clusters/"+url.PathEscape(name)+"/dashboard/"+url.PathEscape(action), map[string]string{"uid": uid}, &op)
	return op, err
}

// Billing reports usage costs; since is a duration, an RFC3339 time or empty for the last reset.
func (c *Client) Billing(ctx context.Context, since string) (cloud.BillingReport, error) {
	path := "/v1/billing"
	if since != "" {
		path += "?since=" + url.QueryEscape(since)
	}
	var result cloud.BillingReport
	_, err := c.request(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func registryPath(name string) string { return "/v1/registries/" + url.PathEscape(name) }

// Registries lists the managed container registries without credentials.
func (c *Client) Registries(ctx context.Context) ([]cloud.Registry, error) {
	var result []cloud.Registry
	_, err := c.request(ctx, http.MethodGet, "/v1/registries", nil, &result)
	return result, err
}

// CreateRegistry requests a registry asynchronously.
func (c *Client) CreateRegistry(ctx context.Context, name string) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodPost, "/v1/registries", map[string]string{"name": name}, &result)
	return result, err
}

// DeleteRegistry requests removal asynchronously; force deletes a registry holding images.
func (c *Client) DeleteRegistry(ctx context.Context, name string, force bool) (cloud.Operation, error) {
	var result cloud.Operation
	_, err := c.request(ctx, http.MethodDelete, registryPath(name)+"?force="+strconv.FormatBool(force), nil, &result)
	return result, err
}

// RegistryCredentials downloads the push or pull account of a registry.
func (c *Client) RegistryCredentials(ctx context.Context, name, access string) (cloud.RegistryCredential, error) {
	var result cloud.RegistryCredential
	_, err := c.request(ctx, http.MethodGet, registryPath(name)+"/credentials?access="+url.QueryEscape(access), nil, &result)
	return result, err
}

// RegistryImages lists the images stored in a registry.
func (c *Client) RegistryImages(ctx context.Context, name string) ([]infra.RegistryImage, error) {
	var result []infra.RegistryImage
	_, err := c.request(ctx, http.MethodGet, registryPath(name)+"/images", nil, &result)
	return result, err
}
