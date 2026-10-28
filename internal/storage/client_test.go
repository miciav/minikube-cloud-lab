package storage

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientSurfacesUnavailableBackend(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	if err := c.Ensure(context.Background(), "pvc-test", 64<<20); err == nil {
		t.Fatal("missing storage server accepted")
	}
}

func TestClientReportsBackendErrors(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "storage-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	ln, e := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "volume is still exported", http.StatusConflict)
	})}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close(); _ = os.Remove(path) })
	if err := NewClient(path).Delete(context.Background(), "pvc-test"); err == nil {
		t.Fatal("backend error discarded")
	}
}
