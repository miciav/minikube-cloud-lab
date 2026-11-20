package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func addDiskCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "disks", Short: "Manage standalone instance data disks"}
	for _, action := range []string{"create", "attach", "detach", "delete"} {
		var size, instance string
		sub := &cobra.Command{Use: action + " NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			d, err := client(cmd).ChangeDisk(cmd.Context(), action, args[0], size, instance)
			if err != nil {
				return err
			}
			switch {
			case action == "attach" && os.Getenv("CLOUD_ROOTLESS") == "true":
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Open on the lab host: cloud disks shell %s %s\n", d.Instance, d.Name)
			case action == "attach":
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: /dev/cloud-disks/%s\n", d.Instance, d.Name)
			default:
				_, err = fmt.Fprintln(cmd.OutOrStdout(), d.Name)
			}
			return err
		}}
		if action == "create" {
			sub.Flags().StringVar(&size, "size", "", "Disk size, for example 1Gi")
			_ = sub.MarkFlagRequired("size")
		}
		if action == "attach" || action == "detach" {
			sub.Flags().StringVar(&instance, "instance", "", "Standalone instance name")
			_ = sub.MarkFlagRequired("instance")
		}
		parent.AddCommand(sub)
	}
	parent.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		disks, err := client(cmd).InstanceDisks(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tBYTES\tINSTANCE")
		for _, d := range disks {
			_, _ = fmt.Fprintf(w, "%s\t%d\t%s\n", d.Name, d.Bytes, d.Instance)
		}
		return w.Flush()
	}})
	root.AddCommand(parent)
}
