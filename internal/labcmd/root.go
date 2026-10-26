// Package labcmd applies the visible teaching manifests through Kubernetes APIs.
package labcmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

type connector func(string, string) (dynamic.Interface, meta.RESTMapper, error)

// NewRoot constructs the lab command tree.
func NewRoot() *cobra.Command { return newRoot(connect) }

func connect(path, context string) (dynamic.Interface, meta.RESTMapper, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.Precedence = filepath.SplitList(path)
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: context}).ClientConfig()
	if err != nil {
		return nil, nil, err
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}
	discover, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, nil, err
	}
	return client, restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discover)), nil
}
func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func newRoot(factory connector) *cobra.Command {
	var path, context string
	root := &cobra.Command{Use: "lab", Short: "Install teaching tools and generate cluster load", SilenceUsage: true, SilenceErrors: true}
	path = os.Getenv("KUBECONFIG")
	if path == "" {
		path = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}
	root.PersistentFlags().StringVar(&path, "kubeconfig", path, "Kubernetes credentials")
	root.PersistentFlags().StringVar(&context, "context", "", "Kubernetes context")
	action := func(cmd *cobra.Command, paths []string, remove bool, replicas *int32) error {
		prefix := "kubectl"
		if cmd.Flags().Changed("kubeconfig") {
			prefix += " --kubeconfig " + quote(path)
		}
		if context != "" {
			prefix += " --context " + quote(context)
		}
		verb := "apply --server-side --field-manager=lab"
		if remove {
			verb = "delete --ignore-not-found"
		}
		visible := "lab/manifests/load.yaml"
		if len(paths) > 1 {
			visible = "lab/manifests/autoscaler/"
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "equivalent: %s %s -f %s\n", prefix, verb, visible)
		if replicas != nil {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "equivalent: %s scale deployment/lab-load --replicas=%d\n", prefix, *replicas)
		}
		client, mapper, err := factory(path, context)
		if err != nil {
			return err
		}
		return applyFiles(cmd.Context(), client, mapper, paths, remove, replicas)
	}
	autoscalerFiles := []string{"autoscaler/rbac.yaml", "autoscaler/cloud-config.yaml", "autoscaler/deployment.yaml"}
	for _, verb := range []string{"install", "uninstall"} {
		parent := &cobra.Command{Use: verb}
		parent.AddCommand(&cobra.Command{Use: "autoscaler", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return action(cmd, autoscalerFiles, verb == "uninstall", nil)
		}})
		root.AddCommand(parent)
	}
	load := &cobra.Command{Use: "load", Short: "Generate resource pressure"}
	var replicas int32
	start := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if replicas < 1 {
			return fmt.Errorf("replicas must be positive")
		}
		var override *int32
		if cmd.Flags().Changed("replicas") {
			override = &replicas
		}
		return action(cmd, []string{"load.yaml"}, false, override)
	}}
	start.Flags().Int32Var(&replicas, "replicas", 4, "Number of load pods")
	load.AddCommand(start, &cobra.Command{Use: "stop", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return action(cmd, []string{"load.yaml"}, true, nil) }})
	root.AddCommand(load)
	return root
}
