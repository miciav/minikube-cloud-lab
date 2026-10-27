package serviceexposure

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func availablePort(t *testing.T) int32 {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return int32(port) // #nosec G115 -- TCP port is within 1..65535.
}

func startTestProxy(t *testing.T, target string) (string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	port := availablePort(t)
	go func() { defer close(done); done <- RunProxy(ctx, []Forward{{ListenPort: port, Target: target}}) }()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("proxy shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("proxy did not stop")
		}
	})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c, err := (&net.Dialer{Timeout: 20 * time.Millisecond}).DialContext(t.Context(), "tcp", addr)
		if err == nil {
			_ = c.Close()
			return addr, cancel
		}
		select {
		case err := <-done:
			t.Fatalf("proxy returned before listening: %v", err)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("proxy never opened its listener")
	return "", cancel
}

func TestProxyForwardsBytesAndHalfClose(t *testing.T) {
	backend, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	go func() {
		for {
			c, err := backend.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				data, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("reply:"), data...))
			}()
		}
	}()
	addr, _ := startTestProxy(t, backend.Addr().String())
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte("\x00hello\xff")); err != nil {
		t.Fatal(err)
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(c)
	if err != nil || string(data) != "reply:\x00hello\xff" {
		t.Fatalf("lost bytes or half-close: %q %v", data, err)
	}
}

func TestProxyPassesTLSUnchanged(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "encrypted-through-relay") }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	addr, _ := startTestProxy(t, server.Listener.Addr().String())
	client := server.Client()
	client.Timeout = time.Second
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil || string(data) != "encrypted-through-relay" {
		t.Fatalf("TLS passthrough: %q %v", data, err)
	}
}

func TestProxyCancellationClosesConnections(t *testing.T) {
	backend, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	go func() {
		for {
			c, err := backend.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); _, _ = io.Copy(c, c) }()
		}
	}()
	addr, cancel := startTestProxy(t, backend.Addr().String())
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	_, _ = c.Write([]byte("x"))
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("connection survived cancellation")
	} else {
		var e net.Error
		if errors.As(err, &e) && e.Timeout() {
			t.Fatal("connection left open after cancellation")
		}
	}
}

func TestProxyRejectsInvalidAndDuplicatePorts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		forwards []Forward
	}{
		{"empty", nil},
		{"zero", []Forward{{0, "127.0.0.1:80"}}},
		{"large", []Forward{{65536, "127.0.0.1:80"}}},
		{"bad-target", []Forward{{30080, "invalid"}}},
		{"target-zero", []Forward{{30080, "127.0.0.1:0"}}},
		{"duplicate", []Forward{{30080, "127.0.0.1:80"}, {30080, "127.0.0.1:81"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := RunProxy(t.Context(), tc.forwards); err == nil {
				t.Fatal("invalid proxy configuration accepted")
			}
		})
	}
}

func TestProxyBindFailureClosesEarlierListeners(t *testing.T) {
	occupied, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	first := availablePort(t)
	occupiedPort := int32(occupied.Addr().(*net.TCPAddr).Port) // #nosec G115 -- TCP listener port is within 1..65535.
	forwards := []Forward{{first, "127.0.0.1:80"}, {occupiedPort, "127.0.0.1:80"}}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := RunProxy(ctx, forwards); err == nil {
		t.Fatal("occupied bind reported success")
	}
	check, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(first))))
	if err != nil {
		t.Fatalf("failed startup leaked earlier listener: %v", err)
	}
	_ = check.Close()
}

func TestProxyTargetFailureDoesNotStopListener(t *testing.T) {
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(availablePort(t))))
	addr, _ := startTestProxy(t, target)
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("unreachable target kept the connection open")
	}
	_ = c.Close()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "recovered") }))
	_ = server.Listener.Close()
	server.Listener, err = (&net.ListenConfig{}).Listen(t.Context(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	server.Start()
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil || string(data) != "recovered" {
		t.Fatalf("target recovery: %q %v", data, err)
	}
}
