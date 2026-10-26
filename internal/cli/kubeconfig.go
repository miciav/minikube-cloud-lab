package cli

import (
	"fmt"
	"os"

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
	if err := clientcmd.ModifyConfig(access, *starting, true); err != nil {
		return err
	}
	return os.Chmod(access.GetDefaultFilename(), 0o600)
}
