package minikube

import (
	"context"
	"fmt"
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubectl/pkg/drain"
	"k8s.io/minikube/pkg/minikube/config"
	"k8s.io/minikube/pkg/minikube/machine"
)

func (i *Infra) removeWorker(ctx context.Context, cc *config.ClusterConfig, name string) error {
	index := -1
	for j, n := range cc.Nodes {
		if config.MachineName(*cc, n) == name {
			index = j
		}
	}
	if index < 0 {
		return fmt.Errorf("unknown machine %s", name)
	}
	if cc.Nodes[index].ControlPlane {
		return fmt.Errorf("cannot remove control plane")
	}
	data, err := i.Kubeconfig(ctx, cc.Name)
	if err != nil {
		return err
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return err
	}
	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	n, err := c.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		d := &drain.Helper{Ctx: ctx, Client: c, Force: true, IgnoreAllDaemonSets: true, DeleteEmptyDirData: true, DisableEviction: true, GracePeriodSeconds: 1, SkipWaitForDeleteTimeoutSeconds: 1, Timeout: 2 * time.Minute, Out: io.Discard, ErrOut: io.Discard}
		if err := drain.RunCordonOrUncordon(d, n, true); err != nil {
			return err
		}
		if err := drain.RunNodeDrain(d, name); err != nil {
			return err
		}
		if err := c.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	api, err := machine.NewAPIClient(options)
	if err != nil {
		return err
	}
	defer func() { _ = api.Close() }()
	if err := removeMachine(ctx, api, name); err != nil {
		return err
	}
	cc.Nodes = append(cc.Nodes[:index], cc.Nodes[index+1:]...)
	return config.SaveProfile(cc.Name, cc)
}
