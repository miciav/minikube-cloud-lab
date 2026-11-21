package cloud

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyFailureDoesNotConfirmDrain(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
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
