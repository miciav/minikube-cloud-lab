package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/nomad-autoscaler/sdk"
)

func TestLabTargetRequestsAbsoluteCapacityWithExpectedCount(t *testing.T) {
	var scaled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/groups/asg-test":
			_, _ = w.Write([]byte(`{"desired":2,"scalingReady":true}`))
		case r.Method == "POST" && r.URL.Path == "/v1/groups/asg-test/scale":
			var request struct {
				Desired, Expected int
				Reason            string
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Desired != 3 ||
				request.Expected != 2 || request.Reason != "higher traffic" {
				http.Error(w, "bad scaling request", http.StatusBadRequest)
				return
			}
			scaled.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := &labTarget{}
	if err := p.SetConfig(map[string]string{"address": server.URL}); err != nil {
		t.Fatal(err)
	}
	config := map[string]string{"id": "asg-test"}
	got, err := p.Status(config)
	if err != nil || !got.Ready || got.Count != 2 {
		t.Fatalf("status: %+v, %v", got, err)
	}
	if err := p.Scale(sdk.ScalingAction{Count: 3, Reason: "higher traffic"}, config); err != nil || !scaled.Load() {
		t.Fatalf("scaling request sent: %t, %v", scaled.Load(), err)
	}
}
