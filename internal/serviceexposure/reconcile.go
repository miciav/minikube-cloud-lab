package serviceexposure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/docker/docker/api/types/container"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Controller-owned annotations describe observed local exposure.
const (
	HostEndpointsAnnotation = "minikube-cloud-lab.io/host-endpoints"
	ExposureErrorAnnotation = "minikube-cloud-lab.io/exposure-error"
)

// Reconcile derives local relays from a complete, successful Service list.
func Reconcile(ctx context.Context, kube kubernetes.Interface, m *Manager, cluster, ip string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	services, err := kube.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	owned, err := m.owned(ctx, cluster, "")
	if err != nil {
		return err
	}
	previous := map[types.UID]map[string]bool{}
	for _, c := range owned {
		if c.Labels[typeLabel] == string(corev1.ServiceTypeLoadBalancer) {
			uid := types.UID(c.Labels[uidLabel])
			if previous[uid] == nil {
				previous[uid] = map[string]bool{}
			}
			previous[uid][c.Labels[ipLabel]] = true
		}
	}
	keep := []types.UID{}
	for _, svc := range services.Items {
		keep = append(keep, svc.UID)
	}
	// Free stale fixed NodePorts before a replacement UID tries to claim them.
	if err := m.Prune(ctx, cluster, keep); err != nil {
		return err
	}
	var failures []error
	for _, svc := range services.Items {
		if err := clearReplacementStatus(ctx, kube, &svc, owned, ip, previous[svc.UID]); err != nil {
			failures = append(failures, err)
			continue
		}
		status := Status{}
		var exposureErr error
		if svc.Spec.Type == corev1.ServiceTypeNodePort || svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			_, exposureErr = serviceForwards(&svc, ip)
			if exposureErr == nil {
				status, exposureErr = m.Ensure(ctx, cluster, &svc, ip)
			}
			if exposureErr != nil && !errors.Is(exposureErr, ErrUnsupported) {
				failures = append(failures, fmt.Errorf("%s/%s: %w", svc.Namespace, svc.Name, exposureErr))
			}
		}
		if err := publish(ctx, kube, &svc, status, exposureErr, previous[svc.UID]); err != nil {
			failures = append(failures, err)
			continue
		}
		if svc.Spec.Type != corev1.ServiceTypeNodePort && svc.Spec.Type != corev1.ServiceTypeLoadBalancer || errors.Is(exposureErr, ErrUnsupported) {
			failures = append(failures, m.Remove(ctx, cluster, svc.UID))
		}
	}
	return errors.Join(failures...)
}

func publish(ctx context.Context, kube kubernetes.Interface, svc *corev1.Service, status Status, exposureErr error, previous map[string]bool) error {
	annotations := map[string]any{HostEndpointsAnnotation: nil, ExposureErrorAnnotation: nil}
	desiredEndpoints, desiredError := "", ""
	if exposureErr != nil {
		desiredError = exposureErr.Error()
		annotations[ExposureErrorAnnotation] = desiredError
	} else if len(status.Ports) > 0 {
		payload, _ := json.Marshal(status.Ports)
		desiredEndpoints = string(payload)
		annotations[HostEndpointsAnnotation] = desiredEndpoints
	}
	client := kube.CoreV1().Services(svc.Namespace)
	if svc.Annotations[HostEndpointsAnnotation] != desiredEndpoints || svc.Annotations[ExposureErrorAnnotation] != desiredError {
		payload, _ := json.Marshal(map[string]any{"metadata": map[string]any{"uid": svc.UID, "resourceVersion": svc.ResourceVersion, "annotations": annotations}})
		updated, err := client.Patch(ctx, svc.Name, types.MergePatchType, payload, metav1.PatchOptions{})
		if err != nil {
			return err
		}
		*svc = *updated
	}
	ingress := make([]corev1.LoadBalancerIngress, 0, len(svc.Status.LoadBalancer.Ingress)+1)
	for _, entry := range svc.Status.LoadBalancer.Ingress {
		if !previous[entry.IP] && (status.IP == "" || entry.IP != status.IP) {
			ingress = append(ingress, entry)
		}
	}
	if status.IP != "" && svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
		ingress = append(ingress, corev1.LoadBalancerIngress{IP: status.IP})
	}
	if len(ingress) == 0 && len(svc.Status.LoadBalancer.Ingress) == 0 {
		return nil
	}
	if reflect.DeepEqual(ingress, svc.Status.LoadBalancer.Ingress) {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"metadata": map[string]any{"uid": svc.UID, "resourceVersion": svc.ResourceVersion}, "status": map[string]any{"loadBalancer": map[string]any{"ingress": ingress}}})
	updated, err := client.Patch(ctx, svc.Name, types.MergePatchType, payload, metav1.PatchOptions{}, "status")
	if err == nil {
		*svc = *updated
	}
	return err
}

// Clear owned ingress while the old container still proves ownership.
func clearReplacementStatus(ctx context.Context, kube kubernetes.Interface, svc *corev1.Service, owned []container.Summary, ip string, previous map[string]bool) error {
	forwards, err := serviceForwards(svc, ip)
	if err != nil {
		return nil
	}
	digest := forwardHash(svc.Spec.Type, forwards)
	for _, c := range owned {
		if c.Labels[uidLabel] == string(svc.UID) && c.Labels[typeLabel] == string(corev1.ServiceTypeLoadBalancer) && c.Labels[hashLabel] != digest {
			return publish(ctx, kube, svc, Status{}, nil, previous)
		}
	}
	return nil
}
