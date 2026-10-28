package cloud

import (
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func (s *Service) clusterRESTConfig(cluster string) (*rest.Config, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: filepath.Join(s.dir, "minikube", "kubeconfig")}, &clientcmd.ConfigOverrides{CurrentContext: cluster}).ClientConfig()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
