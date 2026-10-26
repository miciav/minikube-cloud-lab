package minikube_test

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"

	"k8s.io/klog/v2"
	"k8s.io/minikube/pkg/minikube/out"

	backend "github.com/miciav/minikube-cloud-lab/internal/infra/minikube"
)

func TestInfrastructureDetailsAreHiddenAtDefaultLogLevel(t *testing.T) {
	t.Setenv("MINIKUBE_HOME", os.Getenv("MINIKUBE_HOME"))
	t.Setenv("KUBECONFIG", os.Getenv("KUBECONFIG"))
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	if _, err := backend.New(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	klog.Info("internal --token secret-bootstrap-value")
	out.String("internal --token secret-bootstrap-value")
	klog.Error("infrastructure failure marker")
	slog.Info("cloud accepted request", "requester", "student")
	text := output.String()
	if strings.Contains(text, "secret-bootstrap-value") {
		t.Fatalf("upstream details escaped into default logs: %s", text)
	}
	if !strings.Contains(text, "infrastructure failure marker") || !strings.Contains(text, "cloud accepted request") {
		t.Fatalf("important cloud logs were hidden: %s", text)
	}
}
