package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Backend owns bounded volume filesystems; callers retain their allocation intent.
type Backend interface {
	Ensure(context.Context, string, int64) error
	Delete(context.Context, string) error
}

// Client talks only through the shared local storage socket.
type Client struct{ http *http.Client }

// NewClient connects to the NFS manager over a shared Unix socket.
func NewClient(socket string) *Client {
	return &Client{http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}

// Ensure idempotently creates or recovers a bounded volume.
func (c *Client) Ensure(ctx context.Context, id string, size int64) error {
	return c.request(ctx, http.MethodPut, id, size)
}

// Delete removes an unexported volume and its backing image.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, id, 0)
}

func (c *Client) request(ctx context.Context, method, id string, size int64) error {
	body, err := json.Marshal(struct {
		Bytes int64 `json:"bytes"`
	}{size})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://storage/volumes/"+url.PathEscape(id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("storage unavailable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("storage: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return nil
}
