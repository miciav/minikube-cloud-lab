package labcmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/miciav/minikube-cloud-lab/internal/serviceexposure"
)

func servicesCommand(factory connector, path, context *string) *cobra.Command {
	return &cobra.Command{Use: "services", Short: "Show localhost Service endpoints", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		prefix := "kubectl"
		if cmd.Flags().Changed("kubeconfig") {
			prefix += " --kubeconfig " + quote(*path)
		}
		if *context != "" {
			prefix += " --context " + quote(*context)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "equivalent: %s get services -A -o json\n", prefix)
		client, _, err := factory(*path, *context)
		if err != nil {
			return err
		}
		services, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).List(cmd.Context(), metav1.ListOptions{})
		if err != nil {
			return err
		}
		sort.Slice(services.Items, func(i, j int) bool {
			return services.Items[i].GetNamespace()+"/"+services.Items[i].GetName() < services.Items[j].GetNamespace()+"/"+services.Items[j].GetName()
		})
		table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(table, "NAMESPACE\tNAME\tTYPE\tLOCAL_ENDPOINT\tERROR")
		for _, svc := range services.Items {
			kind, _, _ := unstructured.NestedString(svc.Object, "spec", "type")
			endpoints, problem := serviceEndpoints(svc.GetAnnotations())
			_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", svc.GetNamespace(), svc.GetName(), kind, endpoints, problem)
		}
		return table.Flush()
	}}
}

func serviceEndpoints(annotations map[string]string) (string, string) {
	problem := annotations[serviceexposure.ExposureErrorAnnotation]
	raw := annotations[serviceexposure.HostEndpointsAnnotation]
	if raw == "" || problem != "" {
		return "-", problem
	}
	var ports []serviceexposure.HostPort
	if err := json.Unmarshal([]byte(raw), &ports); err != nil || len(ports) == 0 {
		return "-", "invalid host-endpoints annotation"
	}
	endpoints := make([]string, 0, len(ports))
	for _, port := range ports {
		if port.HostPort < 1 || port.HostPort > 65535 {
			return "-", "invalid host-endpoints port"
		}
		endpoints = append(endpoints, fmt.Sprintf("127.0.0.1:%d", port.HostPort))
	}
	return strings.Join(endpoints, ","), ""
}
