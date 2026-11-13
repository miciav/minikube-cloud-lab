package cloud

import (
	"context"
	"path/filepath"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// clusterRESTConfig selects one cluster from the private kubeconfig with a bounded request time.
func (s *Service) clusterRESTConfig(cluster string) (*rest.Config, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: filepath.Join(s.dir, "minikube", "kubeconfig")}, &clientcmd.ConfigOverrides{CurrentContext: cluster}).ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 5 * time.Second
	return cfg, nil
}

// observeCluster runs a read-only view of a running cluster and discards it if the
// cluster was replaced meanwhile. It never takes the infrastructure operation lock.
func (s *Service) observeCluster(ctx context.Context, cluster string, read func(context.Context, *rest.Config) error) (Cluster, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	before, err := s.monitoringCluster(cluster)
	if err != nil {
		return Cluster{}, err
	}
	cfg, err := s.clusterRESTConfig(cluster)
	if err != nil {
		return Cluster{}, ErrUnavailable
	}
	if err := read(ctx, cfg); err != nil {
		return Cluster{}, err
	}
	after, err := s.monitoringCluster(cluster)
	if err != nil {
		return Cluster{}, err
	}
	if !before.CreatedAt.Equal(after.CreatedAt) {
		return Cluster{}, ErrConflict
	}
	return after, nil
}
