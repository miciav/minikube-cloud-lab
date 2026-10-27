package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/miciav/minikube-cloud-lab/internal/api"
	"github.com/miciav/minikube-cloud-lab/internal/cloud"
	"github.com/miciav/minikube-cloud-lab/internal/externalgrpc"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	protos "github.com/miciav/minikube-cloud-lab/proto"
)

func serveCommand(factory func(string) (infra.Infra, error)) *cobra.Command {
	var dataDir, apiAddr, grpcAddr string
	command := &cobra.Command{Use: "serve", Short: "Run the cloud service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		httpListener, err := (&net.ListenConfig{}).Listen(cmd.Context(), "tcp", apiAddr)
		if err != nil {
			return err
		}
		defer func() { _ = httpListener.Close() }()
		grpcListener, err := (&net.ListenConfig{}).Listen(cmd.Context(), "tcp", grpcAddr)
		if err != nil {
			return err
		}
		defer func() { _ = grpcListener.Close() }()
		logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
		slog.SetDefault(logger)
		inf, err := factory(dataDir)
		if err != nil {
			return err
		}
		svc, err := cloud.NewService(inf, dataDir, logger)
		if err != nil {
			return err
		}
		workerCtx, stopWorker := context.WithCancel(cmd.Context())
		workerDone := make(chan struct{})
		go func() {
			defer close(workerDone)
			if err := inf.RunServiceExposure(workerCtx); err != nil && workerCtx.Err() == nil {
				logger.Error("service exposure stopped", "error", err)
			}
		}()
		defer func() { stopWorker(); <-workerDone }()
		management := &http.Server{Handler: api.NewServer(svc), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
		rpc := grpc.NewServer()
		protos.RegisterCloudProviderServer(rpc, externalgrpc.New(svc, logger))
		failures := make(chan error, 2)
		go func() { failures <- management.Serve(httpListener) }()
		go func() { failures <- rpc.Serve(grpcListener) }()
		logger.Info("cloud ready", "api", httpListener.Addr(), "externalgrpc", grpcListener.Addr())
		var serveErr error
		select {
		case <-cmd.Context().Done():
		case serveErr = <-failures:
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := management.Shutdown(shutdown)
		rpc.Stop()
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, grpc.ErrServerStopped) {
			return serveErr
		}
		return shutdownErr
	}}
	command.Flags().StringVar(&dataDir, "data-dir", filepath.Join(os.Getenv("HOME"), ".minikube-cloud-lab"), "Cloud state directory")
	command.Flags().StringVar(&apiAddr, "api", "127.0.0.1:8080", "Management HTTP listen address")
	command.Flags().StringVar(&grpcAddr, "grpc", "0.0.0.0:9090", "Cluster Autoscaler gRPC listen address")
	return command
}
