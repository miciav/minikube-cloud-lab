package labcmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/miciav/minikube-cloud-lab/internal/api"
)

func moduleStatus(factory connector, path, kubeContext *string, files []string) *cobra.Command {
	root := &cobra.Command{Use: "status", Short: "Show module scope and observed readiness"}
	root.AddCommand(&cobra.Command{Use: "instance-autoscaling", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		h, err := moduleCloudClient().InstanceAutoscalingHealth(cmd.Context())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace("instance-autoscaling lab "+h.State+" "+h.Detail))
		return err
	}})
	root.AddCommand(&cobra.Command{Use: "autoscaler", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, mapper, err := factory(*path, *kubeContext)
		if err != nil {
			return err
		}
		objects, err := readObjects(files)
		if err != nil {
			return err
		}
		var missing []string
		ready := false
		for _, o := range objects {
			gvk := o.GroupVersionKind()
			mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				return err
			}
			live, err := client.Resource(mapping.Resource).Namespace(o.GetNamespace()).Get(cmd.Context(), o.GetName(), metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				missing = append(missing, o.GetKind()+"/"+o.GetName())
				continue
			}
			if err != nil {
				return err
			}
			if o.GetKind() == "Deployment" {
				available, _, _ := unstructured.NestedInt64(live.Object, "status", "availableReplicas")
				observed, _, _ := unstructured.NestedInt64(live.Object, "status", "observedGeneration")
				ready = available > 0 && observed >= live.GetGeneration()
			}
		}
		state := "installing"
		switch {
		case len(missing) == len(objects):
			state = "disabled"
		case len(missing) > 0:
			state = "degraded missing " + strings.Join(missing, ", ")
		case ready:
			state = "ready"
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "autoscaler cluster", state)
		return err
	}})
	root.AddCommand(dashboardCommand("status", factory, path, kubeContext))
	return root
}

func dashboardCommand(action string, factory connector, path, kubeContext *string) *cobra.Command {
	return &cobra.Command{Use: "yakd", Short: "Manage the Kubernetes dashboard", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		kube, _, err := factory(*path, *kubeContext)
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		if action == "status" {
			return dashboardStatus(cmd, kube)
		}
		ns, err := kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, "kube-system", metav1.GetOptions{})
		if err != nil {
			return err
		}
		client := moduleCloudClient()
		clusters, err := client.Clusters(ctx)
		if err != nil {
			return err
		}
		if len(clusters) != 1 {
			return fmt.Errorf("expected one managed cluster")
		}
		op, err := client.SetDashboard(ctx, clusters[0].Name, string(ns.GetUID()), action)
		if err != nil {
			return err
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for op.Status == "RUNNING" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
			op, err = client.Operation(ctx, op.ID)
			if err != nil {
				return err
			}
		}
		if op.Status != "DONE" {
			return fmt.Errorf("dashboard operation %s: %s", op.ID, op.Error)
		}
		return nil
	}}
}

func moduleCloudClient() *api.Client {
	address := os.Getenv("CLOUD_API_URL")
	if address == "" {
		address = "http://127.0.0.1:8080"
	}
	return api.NewClient(address)
}

func dashboardStatus(cmd *cobra.Command, kube dynamic.Interface) error {
	ctx := cmd.Context()
	d, err := kube.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).Namespace("yakd-dashboard").Get(ctx, "yakd-dashboard", metav1.GetOptions{})
	state := "disabled"
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	svc, serr := kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace("yakd-dashboard").Get(ctx, "yakd-dashboard", metav1.GetOptions{})
	if serr != nil && !apierrors.IsNotFound(serr) {
		return serr
	}
	if err == nil || serr == nil {
		state = "degraded"
	}
	if err == nil && serr == nil {
		available, _, _ := unstructured.NestedInt64(d.Object, "status", "availableReplicas")
		state = "installing"
		observed, _, _ := unstructured.NestedInt64(d.Object, "status", "observedGeneration")
		if available > 0 && observed >= d.GetGeneration() {
			state = "degraded missing local endpoint"
			if ep, _ := serviceEndpoints(svc.GetAnnotations()); ep != "-" {
				state = "ready"
			}
		}
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "yakd cluster", state)
	return err
}
