package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func TestInstanceAutoscalingCommands(t *testing.T) {
	service, url := setup(t, nil)
	var probes atomic.Int32
	components := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if probes.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer components.Close()
	service.ConfigureInstanceGroupRouting(components.URL, components.URL, components.URL)
	for _, action := range []string{"prepare", "activate"} {
		if _, err := execute(t, url, "instance-autoscaling", action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	go service.RunInstanceGroups(t.Context())
	file := filepath.Join(t.TempDir(), "bootstrap")
	if err := os.WriteFile(file, []byte("true"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"groups", "create", "web", "--bootstrap", file},
		{"groups", "describe", "web"},
		{"groups", "resize", "web", "--desired", "2"},
		{"groups", "list"},
	} {
		out, err := execute(t, url, args...)
		if err != nil || !strings.Contains(out, "web") || !strings.Contains(out, "DESIRED") {
			t.Fatalf("%v: %q, %v", args, out, err)
		}
	}
	cmd := NewRoot()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("true"))
	cmd.SetArgs([]string{"--api-url", url, "groups", "create", "other", "--bootstrap", "-"})
	if err := cmd.ExecuteContext(t.Context()); err != nil || !strings.Contains(out.String(), "other") {
		t.Fatalf("bootstrap from stdin: %s, %v", &out, err)
	}
	for _, args := range [][]string{
		{"groups", "create", "fail", "--bootstrap", file + ".missing"},
		{"groups", "create", "Bad_Name", "--bootstrap", file},
		{"groups", "describe", "missing"},
		{"groups", "resize", "web", "--desired", "10"},
	} {
		if _, err := execute(t, url, args...); err == nil {
			t.Fatalf("%v succeeded unexpectedly", args)
		}
	}
	for _, args := range [][]string{
		{"groups", "delete", "other"},
		{"instance-autoscaling", "begin-removal"},
		{"instance-autoscaling", "finish-removal"},
	} {
		if _, err := execute(t, url, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestUninstallWaitsWhileDrainProgresses(t *testing.T) {
	for _, stall := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			remaining := []string{"web", "a", "b", "c"}
			poll := func(context.Context, string) (cloud.InstanceAutoscalingStatus, error) {
				count := len(remaining)
				if !stall {
					count = max(0, count-int(time.Since(start)/(2*time.Minute)))
				}
				return cloud.InstanceAutoscalingStatus{Remaining: remaining[:count]}, nil
			}
			err := waitAutoscalingRemoval(t.Context(), remaining, poll)
			if stall && (err == nil || !strings.Contains(err.Error(), "web")) {
				t.Fatalf("stalled cleanup = %v", err)
			}
			if !stall && (err != nil || time.Since(start) != 8*time.Minute) {
				t.Fatalf("progressing cleanup took %s: %v", time.Since(start), err)
			}
		})
	}
}
