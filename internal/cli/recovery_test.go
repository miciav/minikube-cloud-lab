package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func TestExistingReaderCannotSeeNewPrivateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	t.Setenv("KUBECONFIG", path)
	if err := clientcmd.WriteToFile(*clientapi.NewConfig(), path); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- regression fixture intentionally starts publicly readable.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path) // #nosec G304 -- test fixture beneath t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	data, err := fake.New().Kubeconfig(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := mergeCredentials(path, "demo", data); err != nil {
		t.Fatal(err)
	}
	observed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(observed), "client-key-data") {
		t.Fatal("previously public file descriptor can still read the newly written private key (contents omitted)")
	}
}
