package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/miciav/minikube-cloud-lab/internal/api"
	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func setup(t *testing.T, backend infra.Infra) (*cloud.Service, string) {
	t.Helper()
	if backend == nil {
		backend = fake.New()
	}
	s, err := cloud.NewService(backend, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewServer(s))
	t.Cleanup(server.Close)
	return s, server.URL
}

func execute(t *testing.T, url string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRoot()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"--api-url", url}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func wait(t *testing.T, s *cloud.Service, op cloud.Operation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.Wait(ctx, op.ID)
	if err != nil || result.Status != "DONE" {
		t.Fatalf("wait: %+v %v", result, err)
	}
}

func TestCloudCommands(t *testing.T) {
	s, url := setup(t, nil)
	out, err := execute(t, url, "clusters", "create", "demo")
	if err != nil || !strings.Contains(out, "Created cluster demo (1 machine)") {
		t.Fatalf("create: %q %v", out, err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"machine-types", "list"}, "lab-medium"}, {[]string{"clusters", "list"}, "demo"}, {[]string{"clusters", "describe", "demo"}, "RUNNING"}, {[]string{"machines", "list", "--cluster", "demo"}, "student"}, {[]string{"operations", "list"}, "CREATE_CLUSTER"}, {[]string{"operations", "describe", s.Operations()[0].ID}, "student"}} {
		out, err := execute(t, url, tc.args...)
		if err != nil || !strings.Contains(out, tc.want) {
			t.Fatalf("%v: %q %v", tc.args, out, err)
		}
		if strings.Contains(out, "profile") || strings.Contains(out, "minikube node") {
			t.Fatalf("infrastructure vocabulary: %q", out)
		}
	}
	out, err = execute(t, url, "clusters", "delete", "demo")
	if err != nil || !strings.Contains(out, "Deleted cluster demo") {
		t.Fatalf("delete: %q %v", out, err)
	}
}

func TestAsyncAndErrors(t *testing.T) {
	s, url := setup(t, nil)
	out, err := execute(t, url, "clusters", "create", "demo", "--async")
	if err != nil || !strings.HasPrefix(out, "op-") {
		t.Fatalf("async: %q %v", out, err)
	}
	wait(t, s, s.Operations()[0])
	if _, err := execute(t, url, "clusters", "create", "second"); err == nil {
		t.Fatal("accepted second cluster")
	}
	out, err = execute(t, url, "clusters", "delete", "demo", "--async")
	if err != nil || !strings.HasPrefix(out, "op-") {
		t.Fatalf("async delete: %q %v", out, err)
	}
	wait(t, s, s.Operations()[0])
	if _, err := execute(t, url, "clusters", "describe", "missing"); err == nil {
		t.Fatal("missing cluster accepted")
	}
	if _, err := execute(t, url, "clusters", "create"); err == nil {
		t.Fatal("missing name accepted")
	}
}

func TestGetCredentialsMerges(t *testing.T) {
	_, url := setup(t, nil)
	if _, err := execute(t, url, "clusters", "create", "demo"); err != nil {
		t.Fatal(err)
	}
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "populated"}[existing], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nested", "config")
			t.Setenv("KUBECONFIG", path)
			if existing {
				cfg := clientapi.NewConfig()
				cfg.Clusters["other"] = &clientapi.Cluster{Server: "https://other.example"}
				cfg.Contexts["other"] = &clientapi.Context{Cluster: "other"}
				cfg.CurrentContext = "other"
				if err := clientcmd.WriteToFile(*cfg, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := execute(t, url, "clusters", "get-credentials", "demo"); err != nil {
				t.Fatal(err)
			}
			cfg, err := clientcmd.LoadFromFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.CurrentContext != "demo" || cfg.Contexts["demo"].Cluster != "demo" {
				t.Fatalf("context: %+v", cfg)
			}
			if existing && cfg.Contexts["other"] == nil {
				t.Fatal("lost other context")
			}
			if len(cfg.Clusters["demo"].CertificateAuthorityData) == 0 {
				t.Fatal("missing embedded CA")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("credentials mode: %v %v", info, err)
			}
		})
	}
}

type signalWriter struct {
	ready chan struct{}
	once  sync.Once
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

type blockedInfra struct {
	infra.Infra
	started, release chan struct{}
}

func (f *blockedInfra) CreateCluster(ctx context.Context, spec infra.ClusterSpec) error {
	close(f.started)
	select {
	case <-f.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return f.Infra.CreateCluster(ctx, spec)
}

func TestWaitInterruptedLeavesOperationRunning(t *testing.T) {
	backend := &blockedInfra{Infra: fake.New(), started: make(chan struct{}), release: make(chan struct{})}
	s, url := setup(t, backend)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := NewRoot()
	cmd.SetOut(io.Discard)
	waiting := &signalWriter{ready: make(chan struct{})}
	cmd.SetErr(waiting)
	cmd.SetArgs([]string{"--api-url", url, "clusters", "create", "demo"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	<-backend.started
	<-waiting.ready
	cancel()
	err := <-done
	op := s.Operations()[0]
	if err == nil || !strings.Contains(err.Error(), "cloud operations describe "+op.ID) || op.Status != "RUNNING" {
		close(backend.release)
		wait(t, s, op)
		t.Fatalf("interrupted wait: %+v %v", op, err)
	}
	close(backend.release)
	wait(t, s, op)
}

func TestServeShutsDownCleanly(t *testing.T) {
	ready := make(chan struct{})
	root := newRoot(func(string) (infra.Infra, error) { close(ready); return fake.New(), nil })
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"serve", "--data-dir", t.TempDir(), "--api", "127.0.0.1:0", "--grpc", "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	select {
	case <-ready:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not initialize")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestOperationFailureAndWaitErrors(t *testing.T) {
	f := fake.New()
	f.Errors["CreateCluster"] = errors.New("Docker unavailable")
	s, url := setup(t, f)
	_, err := execute(t, url, "clusters", "create", "demo")
	if err == nil || !strings.Contains(err.Error(), "Docker unavailable") {
		t.Fatalf("failed operation: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.Wait(ctx, "missing"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestCommandErrorsPropagate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, args := range [][]string{{"machine-types", "list"}, {"clusters", "list"}, {"clusters", "describe", "demo"}, {"clusters", "delete", "demo"}, {"clusters", "get-credentials", "demo"}, {"machines", "list"}, {"operations", "list"}, {"operations", "describe", "unknown"}} {
		if _, err := execute(t, server.URL, args...); err == nil {
			t.Fatalf("failure swallowed: %v", args)
		}
	}
}

func TestInvalidCredentialsDoNotOverwriteFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	for _, data := range [][]byte{[]byte("broken YAML: ["), []byte("apiVersion: v1\nkind: Config\n")} {
		if err := mergeCredentials(path, "demo", data); err == nil {
			t.Fatal("accepted invalid credentials")
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid credentials created a file: %v", err)
	}
}

func TestServeReportsInitializationFailure(t *testing.T) {
	root := newRoot(func(string) (infra.Infra, error) { return nil, errors.New("backend unavailable") })
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"serve", "--data-dir", t.TempDir(), "--api", "127.0.0.1:0", "--grpc", "127.0.0.1:0"})
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "backend unavailable") {
		t.Fatalf("initialization: %v", err)
	}
}

type exposureBackend struct {
	infra.Infra
	started, stopped chan struct{}
	failure          error
}

func (b *exposureBackend) RunServiceExposure(ctx context.Context) error {
	close(b.started)
	defer close(b.stopped)
	if b.failure != nil {
		return b.failure
	}
	<-ctx.Done()
	return nil
}

func TestServeOwnsExposureWorker(t *testing.T) {
	for _, failure := range []error{nil, errors.New("exposure unavailable")} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			b := &exposureBackend{Infra: fake.New(), started: make(chan struct{}), stopped: make(chan struct{}), failure: failure}
			root := newRoot(func(string) (infra.Infra, error) { return b, nil })
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"serve", "--data-dir", t.TempDir(), "--api", "127.0.0.1:0", "--grpc", "127.0.0.1:0"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- root.ExecuteContext(ctx) }()
			select {
			case <-b.started:
			case <-time.After(time.Second):
				t.Fatal("worker was not started")
			}
			select {
			case err := <-done:
				t.Fatalf("worker stopped daemon: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("daemon did not stop")
			}
			select {
			case <-b.stopped:
			default:
				t.Fatal("worker outlived daemon")
			}
		})
	}
}

func TestVolumeCommands(t *testing.T) {
	s, url := setup(t, nil)
	v, err := s.CreateVolume("pvc-cli", "demo", "default/data", "1Gi", "Retain")
	if err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, url, "volumes", "list")
	if err != nil || !strings.Contains(out, v.ID) || !strings.Contains(out, "default/data") {
		t.Fatalf("list: %q %v", out, err)
	}
	if _, err := execute(t, url, "volumes", "delete", v.ID); err == nil {
		t.Fatal("deleted attached volume")
	}
	if err := s.ReleaseVolumeFor(v.ID, "demo", "default/data", strings.TrimPrefix(v.ID, "pvc-")); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, url, "volumes", "delete", v.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.Volumes()) != 0 {
		t.Fatal("volume remains")
	}
}

func TestServeRejectsInvalidStorageAddress(t *testing.T) {
	t.Setenv("CLOUD_NFS_IP", "invalid")
	root := newRoot(func(string) (infra.Infra, error) { return fake.New(), nil })
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"serve", "--data-dir", t.TempDir(), "--api", "127.0.0.1:0", "--grpc", "127.0.0.1:0"})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := root.ExecuteContext(ctx); err == nil || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("invalid NFS address: %v", err)
	}
}
