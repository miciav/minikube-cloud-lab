package cloud

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/miciav/minikube-cloud-lab/internal/serviceexposure"
)

// DashboardEndpoint identifies only the existing localhost relay, never an arbitrary URL.
type DashboardEndpoint struct {
	Port int `json:"port"`
}

// Dashboard returns the known YAKD Service's endpoint once its deployment is available.
func (s *Service) Dashboard(ctx context.Context, cluster string) (DashboardEndpoint, error) {
	var port int
	_, err := s.observeCluster(ctx, cluster, func(ctx context.Context, cfg *rest.Config) error {
		k, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return ErrUnavailable
		}
		svc, err := k.CoreV1().Services("yakd-dashboard").Get(ctx, "yakd-dashboard", metav1.GetOptions{})
		if err != nil {
			return ErrUnavailable
		}
		if port = dashboardPort(svc); port == 0 {
			return ErrUnavailable
		}
		deployment, err := k.AppsV1().Deployments("yakd-dashboard").Get(ctx, "yakd-dashboard", metav1.GetOptions{})
		if err != nil || deployment.Status.AvailableReplicas < 1 {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return DashboardEndpoint{}, err
	}
	return DashboardEndpoint{Port: port}, nil
}

func dashboardPort(svc *corev1.Service) int {
	var endpoints []serviceexposure.HostPort
	if svc.Spec.Type != corev1.ServiceTypeNodePort || svc.Annotations[serviceexposure.ExposureErrorAnnotation] != "" || json.Unmarshal([]byte(svc.Annotations[serviceexposure.HostEndpointsAnnotation]), &endpoints) != nil {
		return 0
	}
	for _, p := range svc.Spec.Ports {
		if p.Port != 80 || p.NodePort == 0 {
			continue
		}
		for _, e := range endpoints {
			if e.ServicePort == 80 && e.NodePort == p.NodePort && e.HostPort > 0 && e.HostPort <= 65535 {
				return int(e.HostPort)
			}
		}
	}

	return 0
}
