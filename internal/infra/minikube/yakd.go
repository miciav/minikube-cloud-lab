package minikube

import (
	"context"
	"io"

	"k8s.io/minikube/pkg/minikube/assets"
	"k8s.io/minikube/pkg/minikube/config"
)

// The upstream addon helper can exit the daemon; apply its bundled assets through the API instead.
func enableYAKD(ctx context.Context, cc *config.ClusterConfig) error {
	client, err := managedDynamicClient(cc.Name)
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
		if err := applyManifest(ctx, client, body, "cloud-yakd", "YAKD"); err != nil {
			return err
		}
	}
	if cc.Addons == nil {
		cc.Addons = map[string]bool{}
	}
	cc.Addons["yakd"] = true
	return config.SaveProfile(cc.Name, cc)
}
