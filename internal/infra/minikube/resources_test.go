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
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/demo-m02/json") {
			w.Header().Set("Content-Type", "application/json")
			if updated.Load() {
				_, _ = w.Write([]byte(`{"HostConfig":{"CPUPeriod":100000,"CPUQuota":200000,"Memory":2147483648,"MemorySwap":2147483648,"PidsLimit":4096}}`))
			} else {
				_, _ = w.Write([]byte(`{"HostConfig":{"PidsLimit":4096}}`))
			}
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/containers/demo-m02/update") {
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var limits struct{ NanoCpus, CPUPeriod, CPUQuota, Memory, MemorySwap, PidsLimit int64 }
		if err := json.NewDecoder(r.Body).Decode(&limits); err != nil {
			t.Error(err)
		}
		if limits.PidsLimit != 4096 || limits.NanoCpus != 0 || limits.CPUPeriod != 100000 || limits.CPUQuota != 200000 || limits.Memory != 2147483648 || limits.MemorySwap != 2147483648 {
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
	if err := setMachineResources(context.Background(), "demo-m02", infra.MachineShape{CPUs: 2, MemoryMiB: 2048}); err != nil {
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
	if err := setMachineResources(context.Background(), "demo", infra.MachineShape{CPUs: 2, MemoryMiB: 2048}); err == nil {
		t.Fatal("resource failure swallowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := setMachineResources(ctx, "demo", infra.MachineShape{CPUs: 2, MemoryMiB: 2048}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestMachineResourcesRejectIgnoredLimitsAndWarnings(t *testing.T) {
	for _, response := range []string{`{"Warnings":[]}`, `{"Warnings":["CPU controller unavailable"]}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/_ping" {
					w.Header().Set("API-Version", "1.51")
					return
				}
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"HostConfig":{}}`))
					return
				}
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			t.Setenv("DOCKER_HOST", strings.Replace(server.URL, "http://", "tcp://", 1))
			t.Setenv("DOCKER_TLS_VERIFY", "")
			t.Setenv("DOCKER_CERT_PATH", "")
			t.Setenv("DOCKER_API_VERSION", "")
			if err := setMachineResources(context.Background(), "demo", infra.MachineShape{CPUs: 2, MemoryMiB: 2048}); err == nil {
				t.Fatal("accepted unenforced limits")
			}
		})
	}
}

func TestMachineResourcesPreserveExistingNanoCPUs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/_ping" {
			w.Header().Set("API-Version", "1.51")
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"HostConfig":{"NanoCpus":2000000000,"Memory":2147483648,"MemorySwap":2147483648}}`))
			return
		}
		var limits struct{ NanoCpus, CPUPeriod, CPUQuota int64 }
		if err := json.NewDecoder(r.Body).Decode(&limits); err != nil {
			t.Error(err)
		}
		if limits.NanoCpus != 2000000000 || limits.CPUPeriod != 0 || limits.CPUQuota != 0 {
			t.Errorf("conflicting quotas: %+v", limits)
		}
		_, _ = w.Write([]byte(`{"Warnings":[]}`))
	}))
	defer server.Close()
	t.Setenv("DOCKER_HOST", strings.Replace(server.URL, "http://", "tcp://", 1))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	if err := setMachineResources(context.Background(), "demo", infra.MachineShape{CPUs: 2, MemoryMiB: 2048}); err != nil {
		t.Fatal(err)
	}
}
