package minikube

import (
	"context"
	"fmt"
	"io"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"slices"

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

// SetDashboard updates the addon only in the cluster selected by the student's kubeconfig.
func (i *Infra) SetDashboard(ctx context.Context, name, uid string, enabled bool) error {
	libraryMu.Lock()
	defer libraryMu.Unlock()
	if err := i.selectCluster(name); err != nil {
		return err
	}
	cc, err := config.Load(name)
	if err != nil {
		return err
	}
	client, err := managedDynamicClient(name)
	if err != nil {
		return err
	}
	ns, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if uid == "" || string(ns.GetUID()) != uid {
		return fmt.Errorf("selected Kubernetes context does not match managed cluster %s", name)
	}
	if enabled {
		return enableYAKD(ctx, cc)
	}
	for _, resource := range []schema.GroupVersionResource{{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, {Group: "apps", Version: "v1", Resource: "deployments"}, {Version: "v1", Resource: "services"}, {Version: "v1", Resource: "serviceaccounts"}} {
		namespace := "yakd-dashboard"
		if resource.Resource == "clusterrolebindings" {
			namespace = ""
		}
		objects := client.Resource(resource).Namespace(namespace)
		obj, err := objects.Get(ctx, "yakd-dashboard", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		owned := slices.ContainsFunc(obj.GetManagedFields(), func(m metav1.ManagedFieldsEntry) bool {
			return m.Manager == "cloud-yakd" && m.Operation == metav1.ManagedFieldsOperationApply
		})
		if !owned {
			return fmt.Errorf("cannot remove unowned %s/%s", resource.Resource, obj.GetName())
		}
		id := obj.GetUID()
		if err := objects.Delete(ctx, obj.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &id}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	delete(cc.Addons, "yakd")
	return config.SaveProfile(name, cc)
}
