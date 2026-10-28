package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	metricsv1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	mfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"github.com/miciav/minikube-cloud-lab/internal/infra/fake"
)

func monitoringNode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
}

func monitoringMetric(name string, now time.Time) *metricsv1.NodeMetrics {
	return &metricsv1.NodeMetrics{ObjectMeta: metav1.ObjectMeta{Name: name}, Timestamp: metav1.NewTime(now), Window: metav1.Duration{Duration: 15 * time.Second}, Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("128Mi")}}
}

func TestMonitoringQuantitiesAndRequests(t *testing.T) {
	now := time.Now()
	p := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "one", Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}}, InitContainers: []corev1.Container{{Name: "init", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}}}}, Overhead: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")}}}
	snap, err := collectMonitoring(t.Context(), kfake.NewSimpleClientset(monitoringNode("one"), p), monitoringMetrics(monitoringMetric("one", now)), now)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Usage == nil || snap.Usage.CPUMillicores != 250 || snap.Usage.MemoryBytes != 134217728 || snap.Nodes[0].Requested.CPUMillicores != 510 || snap.Nodes[0].Allocatable.MemoryBytes != 2147483648 || !snap.Nodes[0].Ready || snap.CoveredNodes != 1 || snap.MetricsStatus != "available" {
		t.Fatalf("invalid snapshot: %+v %+v", snap, snap.Nodes)
	}
}

func TestMonitoringMissingPartialStaleMetrics(t *testing.T) {
	now := time.Now()
	for _, mode := range []string{"missing", "partial", "stale", "zero", "future"} {
		t.Run(mode, func(t *testing.T) {
			k := kfake.NewSimpleClientset(monitoringNode("one"), monitoringNode("two"))
			m := mfake.NewSimpleClientset()
			metric := monitoringMetric("one", now)
			switch mode {
			case "missing":
				m.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "nodes"}, "")
				})
			case "stale":
				metric.Timestamp = metav1.NewTime(now.Add(-61 * time.Second))
			case "future":
				metric.Timestamp = metav1.NewTime(now.Add(61 * time.Second))
			case "zero":
				metric.Usage = corev1.ResourceList{}
				k = kfake.NewSimpleClientset(monitoringNode("one"))
			}
			if mode != "missing" {
				_ = m.Tracker().Create(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}, metric, "")
			}
			got, err := collectMonitoring(t.Context(), k, m, now)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "zero" {
				if got.Usage == nil || got.Usage.CPUMillicores != 0 || got.MetricsStatus != "available" {
					t.Fatalf("zero lost %+v", got)
				}
				return
			}
			if got.Usage != nil {
				t.Fatal("invented aggregate for incomplete coverage")
			}
			want := "unavailable"
			if mode == "partial" {
				want = "partial"
			}
			if got.MetricsStatus != want {
				t.Fatalf("%s: %+v", want, got)
			}
		})
	}
}

func TestMonitoringPodHealth(t *testing.T) {
	pods := []corev1.Pod{{Status: corev1.PodStatus{Phase: corev1.PodPending}}, {Status: corev1.PodStatus{Phase: corev1.PodFailed}}, {Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}, {Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}, {State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}}}}
	k := kfake.NewSimpleClientset()
	for i := range pods {
		pods[i].Name = string(rune('a' + i))
		_, _ = k.CoreV1().Pods("default").Create(t.Context(), &pods[i], metav1.CreateOptions{})
	}
	s, err := collectMonitoring(t.Context(), k, mfake.NewSimpleClientset(), time.Now())
	if err != nil || s.Pods.Total != 4 || s.Pods.Pending != 1 || s.Pods.Unhealthy != 2 {
		t.Fatalf("health %+v %v", s.Pods, err)
	}
}

func TestMonitoringLifecycleAndCancellation(t *testing.T) {
	s, err := NewService(fake.New(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Monitoring(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	s.state.Clusters = []Cluster{{Name: "demo", Status: "CREATING"}}
	if _, err = s.Monitoring(t.Context(), "demo"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	s.state.Clusters[0].Status = "RUNNING"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.Monitoring(ctx, "demo"); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err = s.Monitoring(t.Context(), "demo"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func monitoringMetrics(v *metricsv1.NodeMetrics) *mfake.Clientset {
	m := mfake.NewSimpleClientset()
	_ = m.Tracker().Create(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}, v, "")
	return m
}

func TestMonitoringPublicSnapshotAndDeletion(t *testing.T) {
	for _, mode := range []string{"success", "nodes-down", "pods-down", "delete", "recreate", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, err := NewService(fake.New(), t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			s.state.Clusters = []Cluster{{Name: "demo", Status: "RUNNING", CreatedAt: time.Now()}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/nodes":
					if mode == "nodes-down" {
						http.Error(w, "down", http.StatusServiceUnavailable)
						return
					}
					_ = json.NewEncoder(w).Encode(&corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, Items: []corev1.Node{*monitoringNode("one")}})
				case "/api/v1/pods":
					if mode == "pods-down" {
						http.Error(w, "down", http.StatusServiceUnavailable)
						return
					}
					_ = json.NewEncoder(w).Encode(&corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}})
				case "/apis/metrics.k8s.io/v1beta1/nodes":
					if mode == "delete" || mode == "recreate" {
						s.mu.Lock()
						if mode == "delete" {
							s.state.Clusters = nil
						} else {
							s.state.Clusters[0].CreatedAt = time.Now().Add(time.Hour)
						}
						s.mu.Unlock()
					}
					if mode == "cancel" {
						cancel()
					}
					_ = json.NewEncoder(w).Encode(&metricsv1.NodeMetricsList{TypeMeta: metav1.TypeMeta{APIVersion: "metrics.k8s.io/v1beta1", Kind: "NodeMetricsList"}, Items: []metricsv1.NodeMetrics{*monitoringMetric("one", time.Now())}})
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			config := clientapi.NewConfig()
			config.Clusters["demo"] = &clientapi.Cluster{Server: server.URL}
			config.Contexts["demo"] = &clientapi.Context{Cluster: "demo"}
			config.CurrentContext = "demo"
			dir := filepath.Join(s.dir, "minikube")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := clientcmd.WriteToFile(*config, filepath.Join(dir, "kubeconfig")); err != nil {
				t.Fatal(err)
			}
			got, err := s.Monitoring(ctx, "demo")
			switch mode {
			case "success":
				if err != nil || got.ClusterState != "RUNNING" || got.Usage == nil {
					t.Fatalf("%+v %v", got, err)
				}
			case "delete":
				if !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
			case "recreate":
				if !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			default:
				if err == nil {
					t.Fatal("lost failure")
				}
			}
		})
	}
}
