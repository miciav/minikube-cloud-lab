package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
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

// liveStop records whether the management API still answered when registries stopped.
type liveStop struct {
	*fake.Infra
	logs   *syncBuffer
	served chan bool
}

func (b liveStop) StopRegistries(ctx context.Context) error {
	api := regexp.MustCompile(`api=(\S+)`).FindStringSubmatch(b.logs.String())
	served := false
	if api != nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+api[1]+"/", nil)
		if err != nil {
			panic(err)
		}
		if resp, err := http.DefaultClient.Do(req); err == nil { // #nosec G107,G704 -- loopback test server.
			_ = resp.Body.Close()
			served = true
		}
	}
	b.served <- served
	return b.Infra.StopRegistries(ctx)
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
	var logs syncBuffer
	backend := liveStop{Infra: f, logs: &logs, served: make(chan bool, 1)}
	root := newRoot(func(string) (infra.Infra, error) { return backend, nil })
	root.SetOut(io.Discard)
	root.SetErr(&logs)
	root.SetArgs([]string{"serve", "--data-dir", dir, "--api", "127.0.0.1:0", "--grpc", "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for !slices.Contains(methods(f), "CreateRegistry") || !strings.Contains(logs.String(), "cloud ready") {
		if time.Now().After(deadline) {
			t.Fatalf("serve not ready: calls %v logs %s", methods(f), logs.String())
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
	if !<-backend.served {
		t.Fatal("management API closed before registries stopped")
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

func TestRegistryCommands(t *testing.T) {
	f := fake.New()
	s, url := setup(t, f)
	out, err := execute(t, url, "registries", "create", "corso")
	if err != nil || !strings.Contains(out, "corso.registry:5000") || !strings.Contains(out, "cloud registries credentials corso --push") || !strings.Contains(out, "cloud registries credentials corso --pull") {
		t.Fatalf("create: %q %v", out, err)
	}
	out, err = execute(t, url, "registries", "list")
	if err != nil || !regexp.MustCompile(`NAME\s+ENDPOINT\s+STATUS\ncorso\s+corso\.registry:5000\s+RUNNING\n`).MatchString(out) {
		t.Fatalf("list: %q %v", out, err)
	}
	push, _ := s.RegistryCredentials("corso", true)
	out, err = execute(t, url, "registries", "credentials", "corso", "--push")
	if want := "endpoint corso.registry:5000\nusername push\npassword " + push.Password + "\n"; err != nil || out != want {
		t.Fatalf("push credentials: %q %v", out, err)
	}
	if out, err := execute(t, url, "registries", "credentials", "corso", "--pull"); err != nil || !strings.Contains(out, "username pull\n") {
		t.Fatalf("pull credentials: %q %v", out, err)
	}
	for _, flags := range [][]string{{}, {"--push", "--pull"}} {
		if _, err := execute(t, url, append([]string{"registries", "credentials", "corso"}, flags...)...); err == nil || !strings.Contains(err.Error(), "--push or --pull") {
			t.Fatalf("credentials %v: %v", flags, err)
		}
	}
	pushed := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	f.SetRegistryImages("corso", infra.RegistryImage{Repository: "web", Tag: "v1", Bytes: 42, Pushed: pushed}, infra.RegistryImage{Repository: "web", Tag: "old"})
	out, err = execute(t, url, "registries", "images", "corso")
	if err != nil || !regexp.MustCompile(`REPOSITORY\s+TAG\s+SIZE\s+PUSHED\nweb\s+v1\s+42\s+`+regexp.QuoteMeta(pushed.Local().Format(time.RFC3339))+`\nweb\s+old\s+0\s+-\n`).MatchString(out) {
		t.Fatalf("images: %q %v", out, err)
	}
	if _, err := execute(t, url, "registries", "delete", "corso"); err == nil || !strings.Contains(err.Error(), "force") {
		t.Fatalf("non-empty delete: %v", err)
	}
	if out, err := execute(t, url, "registries", "delete", "corso", "--force"); err != nil || !strings.Contains(out, "Deleted registry corso") {
		t.Fatalf("delete: %q %v", out, err)
	}
	if len(s.Registries()) != 0 {
		t.Fatalf("registry kept: %+v", s.Registries())
	}
	for _, args := range [][]string{{"create", "Bad_Name"}, {"list", "--api-url", "http://127.0.0.1:1"}, {"images", "missing"}, {"credentials", "missing", "--pull"}} {
		if _, err := execute(t, url, append([]string{"registries"}, args...)...); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}
