package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func addMachineCommands(root *cobra.Command) {
	types := &cobra.Command{Use: "machine-types", Short: "Explore machine shapes"}
	types.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		items, err := client(cmd).MachineTypes(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tCPUS\tMEMORY_MIB")
		for _, kind := range items {
			_, _ = fmt.Fprintf(w, "%s\t%d\t%d\n", kind.Name, kind.CPUs, kind.MemoryMiB)
		}
		return w.Flush()
	}})
	machines := &cobra.Command{Use: "machines", Short: "Inspect cloud machines"}
	var filter string
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		items, err := client(cmd).Machines(cmd.Context(), filter)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tCLUSTER\tMACHINE_TYPE\tSTATUS\tROLE\tCREATED_BY\tCREATED")
		for _, m := range items {
			role := "worker"
			if m.ControlPlane {
				role = "control-plane"
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Name, m.Cluster, m.MachineType, m.Status, role, m.CreatedBy, m.CreatedAt.Local().Format(time.RFC3339))
		}
		return w.Flush()
	}}
	list.Flags().StringVar(&filter, "cluster", "", "Filter by cluster")
	machines.AddCommand(list)
	root.AddCommand(types, machines)
}
