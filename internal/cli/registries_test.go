package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

// stuckRegistries holds every registry create until release closes.
type stuckRegistries struct {
	*fake.Infra
	release chan struct{}
}

func (b stuckRegistries) CreateRegistry(ctx context.Context, spec infra.RegistrySpec) (infra.RegistryInfo, error) {
	<-b.release
	return b.Infra.CreateRegistry(ctx, spec)
}

func methods(f *fake.Infra) []string {
	var out []string
	for _, c := range f.Calls() {
		out = append(out, c.Method)
	}
	return out
}

func TestServeStartsAndStopsRegistries(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A previous cloud died while creating a registry.
	stuck := stuckRegistries{Infra: fake.New(), release: make(chan struct{})}
	previous, err := cloud.NewService(stuck, dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	op, err := previous.CreateRegistry("corso", "student")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(stuck.release)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = previous.Wait(ctx, op.ID)
	}()

	f := fake.New()
	root := newRoot(func(string) (infra.Infra, error) { return f, nil })
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"serve", "--data-dir", dir, "--api", "127.0.0.1:0", "--grpc", "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for !slices.Contains(methods(f), "CreateRegistry") {
		if time.Now().After(deadline) {
			t.Fatalf("registries not started: %v", methods(f))
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not stop")
	}
	calls := methods(f)
	if calls[len(calls)-1] != "StopRegistries" {
		t.Fatalf("registries not stopped: %v", calls)
	}
	if observed, _ := f.Registries(context.Background()); len(observed) != 0 || !f.RegistryData("corso") {
		t.Fatalf("containers %+v", observed)
	}
	restarted, err := cloud.NewService(fake.New(), dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Registries(); len(got) != 1 || got[0].Name != "corso" {
		t.Fatalf("registry record lost: %+v", got)
	}
	ops := restarted.Operations()
	if i := slices.IndexFunc(ops, func(o cloud.Operation) bool { return o.ID == op.ID }); i < 0 || ops[i].Status != "ERROR" {
		t.Fatalf("interrupted create: %+v", ops)
	}
}

func TestServeSurvivesRegistryFailures(t *testing.T) {
	f := fake.New()
	f.Errors["Registries"] = errors.New("docker down")
	f.Errors["StopRegistries"] = errors.New("docker down")
	var logs syncBuffer
	root := newRoot(func(string) (infra.Infra, error) { return f, nil })
	root.SetOut(io.Discard)
	root.SetErr(&logs)
	root.SetArgs([]string{"serve", "--data-dir", t.TempDir(), "--api", "127.0.0.1:0", "--grpc", "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(logs.String(), "cloud ready") {
		if time.Now().After(deadline) {
			t.Fatalf("serve not ready: %s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); !strings.Contains(out, "registries not fully started") || !strings.Contains(out, "registries not fully stopped") {
		t.Fatalf("failures not logged: %s", out)
	}
}

// syncBuffer is a log sink the serve goroutine writes while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
