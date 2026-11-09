package labcmd

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/miciav/minikube-cloud-lab/internal/serviceexposure"
)

func servicesCommand(factory connector, path, context *string) *cobra.Command {
	return &cobra.Command{Use: "services", Short: "Show localhost and node Service endpoints", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, _, err := factory(*path, *context)
		if err != nil {
			return err
		}
		services, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).List(cmd.Context(), metav1.ListOptions{})
		if err != nil {
			return err
		}
		nodes, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "nodes"}).List(cmd.Context(), metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("list nodes: %w", err)
		}
		ips := nodeInternalIPs(nodes.Items)
		sort.Slice(services.Items, func(i, j int) bool {
			return services.Items[i].GetNamespace()+"/"+services.Items[i].GetName() < services.Items[j].GetNamespace()+"/"+services.Items[j].GetName()
		})
		table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(table, "NAMESPACE\tNAME\tTYPE\tLOCAL_ENDPOINT\tNODE_ENDPOINTS\tERROR")
		for _, svc := range services.Items {
			kind, _, _ := unstructured.NestedString(svc.Object, "spec", "type")
			endpoints, problem := serviceEndpoints(svc.GetAnnotations())
			_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", svc.GetNamespace(), svc.GetName(), kind, endpoints, serviceNodeEndpoints(svc, ips), problem)
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

func nodeInternalIPs(nodes []unstructured.Unstructured) []string {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].GetName() < nodes[j].GetName() })
	var ips []string
	for _, node := range nodes {
		addresses, _, _ := unstructured.NestedSlice(node.Object, "status", "addresses")
		for _, raw := range addresses {
			address, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			kind, _, _ := unstructured.NestedString(address, "type")
			ip, _, _ := unstructured.NestedString(address, "address")
			if kind == "InternalIP" && ip != "" {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

func serviceNodeEndpoints(svc unstructured.Unstructured, ips []string) string {
	ports, _, _ := unstructured.NestedSlice(svc.Object, "spec", "ports")
	var endpoints []string
	for _, raw := range ports {
		port, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		nodePort, _, _ := unstructured.NestedInt64(port, "nodePort")
		if nodePort < 1 || nodePort > 65535 {
			continue
		}
		for _, ip := range ips {
			endpoints = append(endpoints, net.JoinHostPort(ip, strconv.FormatInt(nodePort, 10)))
		}
	}
	if len(endpoints) == 0 {
		return "-"
	}
	return strings.Join(endpoints, ",")
}
