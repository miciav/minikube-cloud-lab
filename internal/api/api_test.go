package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/miciav/minikube-cloud-lab/internal/api"
	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func setup(t *testing.T) (*api.Client, *cloud.Service, *fake.Infra, string) {
	t.Helper()
	f := fake.New()
	s, err := cloud.NewService(f, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewServer(s))
	t.Cleanup(server.Close)
	return api.NewClient(server.URL), s, f, server.URL
}

func wait(t *testing.T, s *cloud.Service, op cloud.Operation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := s.Wait(ctx, op.ID)
	if err != nil || result.Status != "DONE" {
		t.Fatalf("wait: %+v %v", result, err)
	}
}

func assertStatus(t *testing.T, err error, status int) {
	t.Helper()
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Status != status {
		t.Fatalf("want %d, got %v", status, err)
	}
}

func TestEveryRouteThroughClient(t *testing.T) {
	c, s, _, url := setup(t)
	ctx := context.Background()
	kinds, err := c.MachineTypes(ctx)
	if err != nil || len(kinds) != 2 || kinds[0].Name != "lab-small" || kinds[1].CPUs != 4 {
		t.Fatalf("types: %+v %v", kinds, err)
	}
	op, err := c.CreateCluster(ctx, "demo", "lab-small", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	clusters, err := c.Clusters(ctx)
	if err != nil || len(clusters) != 1 {
		t.Fatalf("list: %+v %v", clusters, err)
	}
	cluster, err := c.Cluster(ctx, "demo")
	if err != nil || cluster.Name != "demo" || cluster.Status != "RUNNING" {
		t.Fatalf("describe: %+v %v", cluster, err)
	}
	for _, filter := range []string{"", "demo"} {
		machines, err := c.Machines(ctx, filter)
		if err != nil || len(machines) != 1 || !machines[0].ControlPlane {
			t.Fatalf("machines: %+v %v", machines, err)
		}
	}
	ops, err := c.Operations(ctx)
	if err != nil || len(ops) != 1 {
		t.Fatalf("operations: %+v %v", ops, err)
	}
	operation, err := c.Operation(ctx, op.ID)
	if err != nil || operation.Requester != "student" {
		t.Fatalf("operation: %+v %v", operation, err)
	}
	data, err := c.Kubeconfig(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := clientcmd.Load(data); err != nil || cfg.CurrentContext != "demo" {
		t.Fatalf("credentials: %s %v", data, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/clusters/demo/kubeconfig", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "application/yaml" {
		t.Fatal(resp.Header)
	}
	op, err = c.DeleteCluster(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	clusters, err = c.Clusters(ctx)
	if err != nil || len(clusters) != 0 {
		t.Fatalf("deleted: %+v %v", clusters, err)
	}
}

func TestCreateConflict(t *testing.T) {
	c, s, _, _ := setup(t)
	ctx := context.Background()
	op, err := c.CreateCluster(ctx, "demo", "lab-small", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	_, err = c.CreateCluster(ctx, "other", "lab-small", 1, 3)
	assertStatus(t, err, 409)
	if !strings.Contains(err.Error(), "a cluster already exists: demo") {
		t.Fatalf("message: %v", err)
	}
}

func TestMissingResources(t *testing.T) {
	c, _, _, _ := setup(t)
	ctx := context.Background()
	for _, get := range []func() error{func() error { _, err := c.Cluster(ctx, "missing"); return err }, func() error { _, err := c.Operation(ctx, "missing"); return err }, func() error { _, err := c.Machines(ctx, "missing"); return err }, func() error { _, err := c.Kubeconfig(ctx, "missing"); return err }, func() error { _, err := c.DeleteCluster(ctx, "missing"); return err }} {
		assertStatus(t, get(), 404)
	}
}

func TestBadJSON(t *testing.T) {
	_, _, _, url := setup(t)
	for _, body := range []string{"{", `{} {}`, `{"unexpected":true}`, `{"name":"` + strings.Repeat("x", 70000) + `"}`} {
		t.Run(body[:min(12, len(body))], func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url+"/v1/clusters", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status: %d", resp.StatusCode)
			}
		})
	}
}

func TestServiceErrorsMapToHTTP(t *testing.T) {
	c, s, f, _ := setup(t)
	ctx := context.Background()
	_, err := c.CreateCluster(ctx, "demo", "unknown", 1, 3)
	assertStatus(t, err, 400)
	f.Delay = 30 * time.Millisecond
	op, err := c.CreateCluster(ctx, "demo", "lab-small", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Kubeconfig(ctx, "demo")
	assertStatus(t, err, 412)
	wait(t, s, op)
	f.Errors["Kubeconfig"] = errors.New("credentials unavailable")
	_, err = c.Kubeconfig(ctx, "demo")
	assertStatus(t, err, 500)
}

func TestClientTransportAndDecodeErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := api.NewClient(":invalid").Clusters(ctx); err == nil {
		t.Fatal("accepted invalid base URL")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "broken JSON") }))
	defer server.Close()
	if _, err := api.NewClient(server.URL).Clusters(ctx); err == nil {
		t.Fatal("accepted bad response")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := api.NewClient(server.URL).Clusters(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
