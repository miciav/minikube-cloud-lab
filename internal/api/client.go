package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
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
