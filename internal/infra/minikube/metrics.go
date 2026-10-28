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
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/kubeconfig"
)

// metricsManifest pins upstream 0.9.0. The lab kubelet certificates lack IP SANs;
// --kubelet-insecure-tls is restricted to this trusted minikube lab, never production.
//
//go:embed manifests/metrics-server.yaml
var metricsManifest []byte

func enableMetrics(ctx context.Context, cc *config.ClusterConfig) error {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig.PathFromEnv()}, &clientcmd.ConfigOverrides{CurrentContext: cc.Name}).ClientConfig()
	if err != nil {
		return err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(metricsManifest), 4096)
	for {
		o := &unstructured.Unstructured{}
		if err := decoder.Decode(o); errors.Is(err, io.EOF) {
			break
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
		if _, err := client.Resource(resource).Namespace(o.GetNamespace()).Patch(ctx, o.GetName(), types.ApplyPatchType, body, metav1.PatchOptions{FieldManager: "cloud-metrics"}); err != nil {
			return fmt.Errorf("apply Metrics Server %s: %w", o.GetKind(), err)
		}
	}
	return nil
}
