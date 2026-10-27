package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/miciav/minikube-cloud-lab/internal/serviceexposure"
)

func proxyCommand() *cobra.Command {
	var config string
	cmd := &cobra.Command{Use: "service-proxy", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var forwards []serviceexposure.Forward
		if err := json.Unmarshal([]byte(config), &forwards); err != nil {
			return fmt.Errorf("decode proxy config: %w", err)
		}
		return serviceexposure.RunProxy(cmd.Context(), forwards)
	}}
	cmd.Flags().StringVar(&config, "config-json", "", "Internal TCP relay configuration")
	return cmd
}
