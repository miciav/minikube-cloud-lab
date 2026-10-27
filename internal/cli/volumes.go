package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func addVolumeCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "volumes", Short: "Inspect cloud storage"}
	parent.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		items, err := client(cmd).Volumes(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tCLUSTER\tCLAIM\tCAPACITY\tPOLICY\tSTATUS")
		for _, v := range items {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", v.ID, v.Cluster, v.Claim, v.Capacity, v.Policy, v.Status)
		}
		return w.Flush()
	}}, &cobra.Command{Use: "delete ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := client(cmd).DeleteVolume(cmd.Context(), args[0]); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted volume %s\n", args[0])
		return err
	}})
	root.AddCommand(parent)
}
