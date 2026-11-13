package minikube

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/kubeconfig"
)

// metricsManifest pins upstream 0.9.0. The lab kubelet certificates lack IP SANs;
// --kubelet-insecure-tls is restricted to this trusted minikube lab, never production.
//
//go:embed manifests/metrics-server.yaml
var metricsManifest []byte

// managedConfig selects one cluster from the private kubeconfig without its current context.
func managedConfig(name string) (*rest.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig.PathFromEnv()}, &clientcmd.ConfigOverrides{CurrentContext: name}).ClientConfig()
}

func managedClient(name string) (kubernetes.Interface, error) {
	cfg, err := managedConfig(name)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

func managedDynamicClient(name string) (dynamic.Interface, error) {
	cfg, err := managedConfig(name)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}

// applyManifest server-side applies every document of a trusted bundled manifest.
func applyManifest(ctx context.Context, client dynamic.Interface, manifest []byte, manager, what string) error {
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	for {
		o := &unstructured.Unstructured{}
		if err := decoder.Decode(o); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if o.GetKind() == "" {
			continue
		}
		resource, _ := meta.UnsafeGuessKindToResource(o.GroupVersionKind())
		body, err := o.MarshalJSON()
		if err != nil {
			return err
		}
		if _, err := client.Resource(resource).Namespace(o.GetNamespace()).Patch(ctx, o.GetName(), types.ApplyPatchType, body, metav1.PatchOptions{FieldManager: manager}); err != nil {
			return fmt.Errorf("apply %s %s: %w", what, o.GetKind(), err)
		}
	}
}

func enableMetrics(ctx context.Context, cc *config.ClusterConfig) error {
	client, err := managedDynamicClient(cc.Name)
	if err != nil {
		return err
	}
	return applyManifest(ctx, client, metricsManifest, "cloud-metrics", "Metrics Server")
}
