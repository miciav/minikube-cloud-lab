package minikube

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/minikube/pkg/minikube/assets"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/kubeconfig"
)

// The upstream addon helper can exit the daemon; apply its bundled assets through the API instead.
func enableYAKD(ctx context.Context, cc *config.ClusterConfig) error {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig.PathFromEnv()}, &clientcmd.ConfigOverrides{CurrentContext: cc.Name}).ClientConfig()
	if err != nil {
		return err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	addon := assets.Addons["yakd"]
	addonConfig := *cc
	// The cluster's registry.k8s.io override is for Kubernetes, not the GHCR addon image.
	addonConfig.KubernetesConfig.ImageRepository = ""
	data := assets.GenerateTemplateData(addon, &addonConfig, assets.NetworkInfo{}, addon.Images, nil, true)
	for _, asset := range addon.Assets {
		var body []byte
		if asset.IsTemplate() {
			rendered, err := asset.Evaluate(data)
			if err != nil {
				return err
			}
			body, err = io.ReadAll(rendered)
			if err != nil {
				return err
			}
		} else {
			body, err = asset.ReadFile(asset.GetSourcePath())
			if err != nil {
				return err
			}
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(body), 4096).Decode(obj); err != nil {
			return err
		}
		resource, _ := meta.UnsafeGuessKindToResource(obj.GroupVersionKind())
		patch, err := obj.MarshalJSON()
		if err != nil {
			return err
		}
		if _, err := client.Resource(resource).Namespace(obj.GetNamespace()).Patch(ctx, obj.GetName(), types.ApplyPatchType, patch, metav1.PatchOptions{FieldManager: "cloud-yakd"}); err != nil {
			return fmt.Errorf("apply YAKD %s: %w", obj.GetKind(), err)
		}
	}
	if cc.Addons == nil {
		cc.Addons = map[string]bool{}
	}
	cc.Addons["yakd"] = true
	return config.SaveProfile(cc.Name, cc)
}
