package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func operationTable(cmd *cobra.Command, ops []cloud.Operation) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tTYPE\tTARGET\tREQUESTER\tSTATUS\tDURATION\tSTARTED\tERROR")
	for _, op := range ops {
		end := time.Now()
		if op.EndedAt != nil {
			end = *op.EndedAt
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", op.ID, op.Type, op.Target, op.Requester, op.Status, end.Sub(op.StartedAt).Round(time.Second), op.StartedAt.Local().Format(time.RFC3339), op.Error)
	}
	return w.Flush()
}

func addOperationCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "operations", Short: "Inspect the request registry"}
	parent.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ops, err := client(cmd).Operations(cmd.Context())
		if err != nil {
			return err
		}
		return operationTable(cmd, ops)
	}}, &cobra.Command{Use: "describe ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		op, err := client(cmd).Operation(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return operationTable(cmd, []cloud.Operation{op})
	}})
	root.AddCommand(parent)
}
