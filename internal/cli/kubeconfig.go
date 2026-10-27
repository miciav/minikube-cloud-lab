package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"
)

func mergeCredentials(path, name string, data []byte) error {
	source, err := clientcmd.Load(data)
	if err != nil {
		return err
	}
	context := source.Contexts[source.CurrentContext]
	if context == nil || source.Clusters[context.Cluster] == nil || source.AuthInfos[context.AuthInfo] == nil {
		return fmt.Errorf("cloud returned incomplete credentials")
	}
	access := clientcmd.NewDefaultPathOptions()
	access.GlobalFile = path
	starting, err := access.GetStartingConfig()
	if err != nil {
		return err
	}
	starting.Clusters[name] = source.Clusters[context.Cluster]
	starting.AuthInfos[name] = source.AuthInfos[context.AuthInfo]
	starting.Contexts[name] = &clientapi.Context{Cluster: name, AuthInfo: name, Namespace: context.Namespace}
	starting.CurrentContext = name
	merged, err := clientcmd.Write(*starting)
	if err != nil {
		return err
	}
	target := access.GetDefaultFilename()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".kubeconfig-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(merged); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}
