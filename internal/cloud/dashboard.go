package cloud

import (
	"context"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/miciav/minikube-cloud-lab/internal/serviceexposure"
)

// DashboardEndpoint identifies only the existing localhost relay, never an arbitrary URL.
type DashboardEndpoint struct {
	Port int `json:"port"`
}

// Dashboard returns the known YAKD Service's endpoint once its deployment is available.
func (s *Service) Dashboard(ctx context.Context, cluster string) (DashboardEndpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	before, err := s.monitoringCluster(cluster)
	if err != nil {
		return DashboardEndpoint{}, err
	}
	cfg, err := s.clusterRESTConfig(cluster)
	if err != nil {
		return DashboardEndpoint{}, ErrUnavailable
	}
	cfg.Timeout = 5 * time.Second
	k, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return DashboardEndpoint{}, ErrUnavailable
	}
	svc, err := k.CoreV1().Services("yakd-dashboard").Get(ctx, "yakd-dashboard", metav1.GetOptions{})
	if err != nil {
		return DashboardEndpoint{}, ErrUnavailable
	}
	port := dashboardPort(svc)
	if port == 0 {
		return DashboardEndpoint{}, ErrUnavailable
	}
	deployment, err := k.AppsV1().Deployments("yakd-dashboard").Get(ctx, "yakd-dashboard", metav1.GetOptions{})
	if err != nil || deployment.Status.AvailableReplicas < 1 {
		return DashboardEndpoint{}, ErrUnavailable
	}
	after, err := s.monitoringCluster(cluster)
	if err != nil {
		return DashboardEndpoint{}, err
	}
	if !before.CreatedAt.Equal(after.CreatedAt) {
		return DashboardEndpoint{}, ErrConflict
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
