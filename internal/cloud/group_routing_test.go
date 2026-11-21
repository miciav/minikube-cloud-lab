package cloud

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func TestProxyFailureDoesNotConfirmDrain(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{groupProxy: "http://" + listener.Addr().String()}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if s.groupBackendAbsent(t.Context(), "asg-web", "http://172.30.0.200:8080") {
		t.Fatal("cannot confirm deregistration from a refused admin connection")
	}
}

func TestProbeDistinguishesHTTPFailureFromUnknownHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if result := groupMemberHealth(t.Context(), server.URL, nil, "", false); result != healthFailed {
		t.Fatalf("HTTP 503 should be a confirmed failure, got %v", result)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result := groupMemberHealth(ctx, server.URL, nil, "", true); result != healthUnknown {
		t.Fatalf("cancelled probe should be indeterminate, got %v", result)
	}
}

func TestGroupRoutingPolicyAndDrain(t *testing.T) {
	ctx := t.Context()
	var urlForTarget atomic.Value
	var proxyMode atomic.Int32
	var reloads atomic.Int32
	var notReady atomic.Bool
	components := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/http/services/"):
			switch proxyMode.Load() {
			case 1:
				http.NotFound(w, r)
			case 2:
				w.WriteHeader(http.StatusServiceUnavailable)
			default:
				backend, _ := urlForTarget.Load().(string)
				status := "UP"
				if proxyMode.Load() == 3 {
					status = "DOWN"
				}
				_, _ = fmt.Fprintf(w, `{"loadBalancer":{"servers":[{"url":%q}]},"serverStatus":{%q:%q}}`, backend, backend, status)
			}
		case r.URL.Path == "/v1/agent/reload":
			if r.Method != http.MethodPut || reloads.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		case r.URL.Path == "/-/ready" && notReady.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer components.Close()
	urlForTarget.Store(components.URL)
	addr, err := url.Parse(components.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(addr.Port())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewService(fake.New(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareInstanceAutoscaling(); err == nil {
		t.Fatal("preparation requires configured services")
	}
	s.ConfigureInstanceGroupRouting(components.URL, components.URL, components.URL)
	if err := s.PrepareInstanceAutoscaling(); err != nil {
		t.Fatal(err)
	}
	notReady.Store(true)
	if err := s.ActivateReadyInstanceAutoscaling(ctx); err == nil {
		t.Fatal("activated while metrics service was unavailable")
	}
	notReady.Store(false)
	if err := s.ActivateReadyInstanceAutoscaling(ctx); err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateGroup(GroupSpec{
		Name: "web", Image: "debian-12", MachineType: "lab-small",
		Bootstrap: "true", Min: 1, Desired: 1, Max: 2, Port: port, HealthPath: "/health", TargetRPS: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.reconcileGroups(ctx)
	waitGroupOperation(t, s)
	member := s.Instances()[0]
	s.mu.Lock()
	next := s.state.clone()
	next.Instances[0].IP = addr.Hostname()
	next.Instances[0].BootedAt = time.Now().Add(-time.Minute)
	err = s.store(next)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.observeGroupRouting(ctx); err != nil {
		t.Fatal(err)
	}
	s.reconcileGroups(ctx)
	routes := filepath.Join(s.groupFiles(), "traefik", "groups.yml")
	checkGroupFile(t, routes, components.URL)
	if err := s.publishGroupPolicies(ctx); err == nil {
		t.Fatal("reload should report 503")
	}
	for range 2 {
		if err := s.publishGroupPolicies(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if reloads.Load() != 2 {
		t.Fatalf("identical policies should not reload: %d", reloads.Load())
	}
	checkGroupFile(t, filepath.Join(s.groupFiles(), "policies", "groups.hcl"), g.ID)

	proxyMode.Store(3)
	if err := s.observeGroupRouting(ctx); err != nil || s.Instances()[0].FailedHealthAt.IsZero() {
		t.Fatalf("confirmed DOWN not observed: %v", err)
	}
	proxyMode.Store(0)
	if err := s.observeGroupRouting(ctx); err != nil || !s.Instances()[0].FailedHealthAt.IsZero() {
		t.Fatalf("recovered health not observed: %v", err)
	}

	if err := s.BeginInstanceAutoscalingRemoval(); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareInstanceAutoscaling(); err == nil {
		t.Fatal("prepared during removal")
	}
	s.reconcileGroups(ctx)
	s.reconcileGroups(ctx)
	if data, err := os.ReadFile(routes); err != nil || strings.Contains(string(data), components.URL) { // #nosec G304 -- generated file under t.TempDir.
		t.Fatalf("retiring member remains in routing file: %s, %v", data, err)
	}
	for _, mode := range []int32{0, 2} {
		proxyMode.Store(mode)
		if err := s.observeGroupRouting(ctx); err != nil || !s.Instances()[0].DrainUntil.IsZero() {
			t.Fatalf("drain started without confirmation with proxy mode %d: %v", mode, err)
		}
	}
	proxyMode.Store(1)
	if err := s.observeGroupRouting(ctx); err != nil || s.Instances()[0].DrainUntil.IsZero() {
		t.Fatalf("Traefik did not confirm drain: %v", err)
	}
	s.reconcileGroups(ctx)
	waitGroupOperation(t, s)
	if _, err := s.Instance(member.Name); err == nil {
		t.Fatal("drained instance remains")
	}
	s.reconcileGroups(ctx)
	if err := s.publishGroupPolicies(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInstanceAutoscalingRemoval(); err != nil {
		t.Fatal(err)
	}
	if err := s.publishGroupPolicies(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(routes); !os.IsNotExist(err) {
		t.Fatalf("routing config still exists: %v", err)
	}
}

func checkGroupFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- generated fixture path.
	if err != nil || !strings.Contains(string(data), want) {
		t.Fatalf("missing %q in %s: %s, %v", want, path, data, err)
	}
}

func waitGroupOperation(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	op, err := s.Wait(ctx, s.Operations()[0].ID)
	if err != nil || op.Status != "DONE" {
		t.Fatalf("operation %s: %+v, %v", op.ID, op, err)
	}
}
