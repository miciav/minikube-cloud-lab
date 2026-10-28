// Command storage-nfs manages persistent bounded filesystems for Ganesha.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/miciav/minikube-cloud-lab/internal/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	if err := storage.Run(ctx, "/export"); err != nil {
		log.Print(err)
		stop()
		os.Exit(1)
	}
}
