package minikube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

func TestMachineResourcesReachDockerAPI(t *testing.T) {
	var updated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("API-Version", "1.51")
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/containers/demo-m02/update") {
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var limits struct{ NanoCpus, Memory, MemorySwap int64 }
		if err := json.NewDecoder(r.Body).Decode(&limits); err != nil {
			t.Error(err)
		}
		if limits.NanoCpus != 2000000000 || limits.Memory != 4294967296 || limits.MemorySwap != 4294967296 {
			t.Errorf("wrong machine limits: %+v", limits)
		}
		updated.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Warnings":[]}`))
	}))
	defer server.Close()
	t.Setenv("DOCKER_HOST", strings.Replace(server.URL, "http://", "tcp://", 1))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	if err := setMachineResources(context.Background(), "demo-m02", infra.MachineShape{CPUs: 2, MemoryMiB: 4096}); err != nil {
		t.Fatal(err)
	}
	if !updated.Load() {
		t.Fatal("Docker limits were never applied")
	}
}

func TestMachineResourceFailuresPropagate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("API-Version", "1.51")
			return
		}
		http.Error(w, `{"message":"limits unavailable"}`, http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("DOCKER_HOST", strings.Replace(server.URL, "http://", "tcp://", 1))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	if err := setMachineResources(context.Background(), "demo", infra.MachineShape{CPUs: 2, MemoryMiB: 4096}); err == nil {
		t.Fatal("resource failure swallowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := setMachineResources(ctx, "demo", infra.MachineShape{CPUs: 2, MemoryMiB: 4096}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
