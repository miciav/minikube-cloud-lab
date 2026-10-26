// Command lab applies teaching tools and workload manifests to Kubernetes.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/miciav/minikube-cloud-lab/internal/labcmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := labcmd.NewRoot().ExecuteContext(ctx)
	stop()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
