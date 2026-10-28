package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func addVolumeCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "volumes", Short: "Inspect cloud storage"}
	var capacity string
	create := &cobra.Command{Use: "create", Short: "Allocate an unclaimed Retain volume", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		v, err := client(cmd).CreateVolume(cmd.Context(), capacity)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), v.ID)
		return err
	}}
	create.Flags().StringVar(&capacity, "capacity", "", "Virtual disk capacity, for example 2Gi")
	_ = create.MarkFlagRequired("capacity")
	parent.AddCommand(create)
	var cluster string
	publish := &cobra.Command{Use: "publish ID", Short: "Publish an unused volume as a static PV", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := client(cmd).PublishVolume(cmd.Context(), args[0], cluster); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Published volume %s in %s\n", args[0], cluster)
		return err
	}}
	publish.Flags().StringVar(&cluster, "cluster", "", "Managed cluster name")
	_ = publish.MarkFlagRequired("cluster")
	parent.AddCommand(publish, &cobra.Command{Use: "unpublish ID", Short: "Remove an unused PV, retaining its data", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := client(cmd).UnpublishVolume(cmd.Context(), args[0]); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Unpublished volume %s\n", args[0])
		return err
	}})
	parent.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		items, err := client(cmd).Volumes(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tCLUSTER\tCLAIM\tCAPACITY\tPOLICY\tSTATUS\tENFORCED")
		for _, v := range items {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%t\n", v.ID, v.Cluster, v.Claim, v.Capacity, v.Policy, v.Status, v.Enforced)
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
