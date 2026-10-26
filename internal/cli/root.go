// Package cli implements the student's cloud command and the cloud daemon.
package cli

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/miciav/minikube-cloud-lab/internal/api"
	"github.com/miciav/minikube-cloud-lab/internal/infra"
	backend "github.com/miciav/minikube-cloud-lab/internal/infra/minikube"
)

// NewRoot constructs the cloud command tree.
func NewRoot() *cobra.Command {
	return newRoot(func(dir string) (infra.Infra, error) { return backend.New(dir) })
}

func newRoot(factory func(string) (infra.Infra, error)) *cobra.Command {
	root := &cobra.Command{Use: "cloud", Short: "Manage clusters and machines in your local cloud", SilenceUsage: true, SilenceErrors: true}
	url := os.Getenv("CLOUD_API_URL")
	if url == "" {
		url = "http://127.0.0.1:8080"
	}
	root.PersistentFlags().String("api-url", url, "Cloud management API URL")
	root.AddCommand(serveCommand(factory))
	addClusterCommands(root)
	addMachineCommands(root)
	addOperationCommands(root)
	return root
}

func client(cmd *cobra.Command) *api.Client {
	url, _ := cmd.Root().PersistentFlags().GetString("api-url")
	return api.NewClient(url)
}

func defaultKubeconfig() string {
	if path := os.Getenv("KUBECONFIG"); path != "" {
		return path
	}
	return filepath.Join(os.Getenv("HOME"), ".kube", "config")
}
