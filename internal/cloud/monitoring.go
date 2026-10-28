package cloud

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	podresource "k8s.io/component-helpers/resource"
	metricsv1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

// ErrUnavailable reports an inaccessible cluster without exposing connection credentials.
var ErrUnavailable = errors.New("cluster data unavailable")

// Resources expresses CPU in millicores and memory in bytes.
type Resources struct {
	CPUMillicores int64 `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
}

// Usage is a timestamped observation, never a resource request.
type Usage struct {
	Resources
	Timestamp time.Time `json:"timestamp"`
	Window    string    `json:"window"`
}

// NodeMonitoring keeps requested, allocatable and measured resources separate.
type NodeMonitoring struct {
	Name        string    `json:"name"`
	Ready       bool      `json:"ready"`
	Allocatable Resources `json:"allocatable"`
	Requested   Resources `json:"requested"`
	Usage       *Usage    `json:"usage"`
}

// PodHealth counts each unhealthy pod once, including failed init containers.
type PodHealth struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Unhealthy int `json:"unhealthy"`
}

// MonitoringSnapshot is a live view; missing or stale metrics remain null.
type MonitoringSnapshot struct {
	ObservedAt    time.Time        `json:"observedAt"`
	ClusterState  ClusterStatus    `json:"clusterState"`
	Nodes         []NodeMonitoring `json:"nodes"`
	Pods          PodHealth        `json:"pods"`
	Usage         *Resources       `json:"usage"`
	CoveredNodes  int              `json:"coveredNodes"`
	MetricsStatus string           `json:"metricsStatus"`
	MetricsReason string           `json:"metricsReason"`
}

// Monitoring reads cluster data without acquiring the infrastructure operation lock.
func (s *Service) Monitoring(ctx context.Context, cluster string) (MonitoringSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	before, err := s.monitoringCluster(cluster)
	if err != nil {
		return MonitoringSnapshot{}, err
	}
	cfg, err := s.clusterRESTConfig(cluster)
	if err != nil {
		return MonitoringSnapshot{}, ErrUnavailable
	}
	cfg.Timeout = 5 * time.Second
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return MonitoringSnapshot{}, ErrUnavailable
	}
	m, err := metrics.NewForConfig(cfg)
	if err != nil {
		return MonitoringSnapshot{}, ErrUnavailable
	}
	snap, err := collectMonitoring(ctx, kube, m, time.Now().UTC())
	if err != nil {
		return MonitoringSnapshot{}, err
	}
	after, err := s.monitoringCluster(cluster)
	if err != nil {
		return MonitoringSnapshot{}, err
	}
	if !before.CreatedAt.Equal(after.CreatedAt) {
		return MonitoringSnapshot{}, ErrConflict
	}
	snap.ClusterState = after.Status
	return snap, nil
}

func (s *Service) monitoringCluster(name string) (Cluster, error) {
	c, err := s.Cluster(name)
	if err != nil {
		return c, err
	}
	if c.Status != "RUNNING" {
		return c, ErrConflict
	}
	return c, nil
}

func resources(r corev1.ResourceList) Resources {
	return Resources{CPUMillicores: r.Cpu().MilliValue(), MemoryBytes: r.Memory().Value()}
}

func collectMonitoring(ctx context.Context, k kubernetes.Interface, m metrics.Interface, now time.Time) (MonitoringSnapshot, error) {
	s := MonitoringSnapshot{ObservedAt: now, Nodes: []NodeMonitoring{}, MetricsStatus: "unavailable", MetricsReason: "Metrics API unavailable"}
	nodes, err := k.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return s, ErrUnavailable
	}
	pods, err := k.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return s, ErrUnavailable
	}
	requests := map[string]Resources{}
	for _, p := range pods.Items {
		s.Pods.Total++
		if p.Status.Phase == corev1.PodPending {
			s.Pods.Pending++
		}
		if unhealthyPod(p) {
			s.Pods.Unhealthy++
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		r := resources(podresource.PodRequests(&p, podresource.PodResourcesOptions{}))
		v := requests[p.Spec.NodeName]
		v.CPUMillicores += r.CPUMillicores
		v.MemoryBytes += r.MemoryBytes
		requests[p.Spec.NodeName] = v
	}
	for _, n := range nodes.Items {
		v := NodeMonitoring{Name: n.Name, Allocatable: resources(n.Status.Allocatable), Requested: requests[n.Name]}
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady {
				v.Ready = c.Status == corev1.ConditionTrue
			}
		}
		s.Nodes = append(s.Nodes, v)
	}
	mm, err := m.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	if err != nil {
		return s, nil
	}
	mergeNodeMetrics(&s, mm.Items, now)
	return s, nil
}

func mergeNodeMetrics(s *MonitoringSnapshot, items []metricsv1.NodeMetrics, now time.Time) {
	measured := map[string]*Usage{}
	for _, v := range items {
		cpu, cpuOK := v.Usage[corev1.ResourceCPU]
		memory, memoryOK := v.Usage[corev1.ResourceMemory]
		if !cpuOK || !memoryOK || cpu.Sign() < 0 || memory.Sign() < 0 {
			continue
		}
		age := now.Sub(v.Timestamp.Time)
		if age > 60*time.Second || age < -30*time.Second || v.Window.Duration <= 0 {
			continue
		}
		measured[v.Name] = &Usage{Resources: resources(v.Usage), Timestamp: v.Timestamp.Time, Window: v.Window.Duration.String()}
	}
	total := Resources{}
	for i := range s.Nodes {
		v := measured[s.Nodes[i].Name]
		s.Nodes[i].Usage = v
		if v != nil {
			s.CoveredNodes++
			total.CPUMillicores += v.CPUMillicores
			total.MemoryBytes += v.MemoryBytes
		}
	}
	s.MetricsReason = "Metrics data incomplete or stale"
	if s.CoveredNodes > 0 {
		s.MetricsStatus = "partial"
	}
	if s.CoveredNodes == len(s.Nodes) && len(s.Nodes) > 0 {
		s.Usage = &total
		s.MetricsStatus = "available"
		s.MetricsReason = ""
	}
}

func unhealthyPod(p corev1.Pod) bool {
	if p.Status.Phase == corev1.PodSucceeded {
		return false
	}
	if p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodUnknown {
		return true
	}
	for _, statuses := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, c := range statuses {
			if c.State.Terminated != nil && c.State.Terminated.ExitCode != 0 {
				return true
			}
			if c.State.Waiting != nil {
				switch c.State.Waiting.Reason {
				case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "InvalidImageName":
					return true
				}
			}
		}
	}
	return false
}
