package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newResizeCommand() *cobra.Command {
	var nodes int
	var asyncResize bool
	resize := &cobra.Command{Use: "resize NAME --nodes N", Short: "Grow a cluster and set min=max=N; shrinking is not supported", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := client(cmd)
		ops, err := c.ResizeCluster(cmd.Context(), args[0], nodes)
		if err != nil {
			return err
		}
		for _, op := range ops {
			if asyncResize {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), op.ID); err != nil {
					return err
				}
			} else if err := waitOperation(cmd, c, op.ID); err != nil {
				return err
			}
		}
		if asyncResize {
			return nil
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Resized cluster %s to %d nodes (min=max=%d)\n", args[0], nodes, nodes)
		return err
	}}
	resize.Flags().IntVar(&nodes, "nodes", 0, "Total nodes including the control plane; existing machine type is preserved")
	_ = resize.MarkFlagRequired("nodes")
	resize.Flags().BoolVar(&asyncResize, "async", false, "Return new worker operation IDs immediately")
	return resize
}
