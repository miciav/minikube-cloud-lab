package minikube

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	dockerclient "github.com/docker/docker/client"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/minikube/pkg/minikube/config"

	"github.com/miciav/minikube-cloud-lab/internal/serviceexposure"
)

func (i *Infra) exposureClient(name string) (kubernetes.Interface, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: filepath.Join(i.home, "kubeconfig")}, &clientcmd.ConfigOverrides{CurrentContext: name}).ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 5 * time.Second
	return kubernetes.NewForConfig(cfg)
}

// RunServiceExposure owns the polling worker until the daemon cancels it.
func (i *Infra) RunServiceExposure(ctx context.Context) error {
	if i.network == "" {
		<-ctx.Done()
		return nil
	}
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		// Capacity operations hold the same lock; exposure catches up after they finish.
		if libraryMu.TryLock() {
			tick, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := i.reconcileServices(tick, client)
			cancel()
			libraryMu.Unlock()
			if err != nil && ctx.Err() == nil {
				slog.Warn("service exposure will retry", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (i *Infra) reconcileServices(ctx context.Context, client *dockerclient.Client) error {
	if err := i.selectCluster(""); err != nil {
		return err
	}
	entries, err := os.ReadDir(config.ProfileFolderPath(""))
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := i.selectCluster(entry.Name()); err != nil {
			return err
		}
		profile, err := config.LoadProfile(entry.Name())
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, node := range profile.Config.Nodes {
			if !node.ControlPlane || node.IP == "" {
				continue
			}
			kube, err := i.exposureClient(entry.Name())
			if err != nil {
				failures = append(failures, err)
				break
			}
			hostname, err := os.Hostname()
			if err != nil {
				return err
			}
			cloud, err := client.ContainerInspect(ctx, hostname)
			if err != nil {
				failures = append(failures, err)
				break
			}
			manager := serviceexposure.NewManager(client, i.network, cloud.Image)
			failures = append(failures, serviceexposure.Reconcile(ctx, kube, manager, entry.Name(), node.IP))
			break
		}
	}
	return errors.Join(failures...)
}

func (i *Infra) deleteServiceExposure(ctx context.Context, name string) error {
	if i.network == "" {
		return nil
	}
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return serviceexposure.NewManager(client, i.network, "").DeleteCluster(ctx, name)
}
