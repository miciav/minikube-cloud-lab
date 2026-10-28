package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func addStorageCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "storage", Short: "Inspect cloud storage capacity"}
	parent.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		status, err := client(cmd).Storage(cmd.Context())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "TOTAL\tALLOCATED\tAVAILABLE\n%s\t%s\t%s\n", status.Total, status.Allocated, status.Available)
		return err
	}})
	root.AddCommand(parent)
}
