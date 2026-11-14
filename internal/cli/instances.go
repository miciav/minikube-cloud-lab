package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/miciav/minikube-cloud-lab/internal/api"
	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func addImageCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "images", Short: "Explore standalone instance images"}
	parent.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		images, err := client(cmd).Images(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tSSH_USER\tDESCRIPTION")
		for _, image := range images {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", image.Name, image.SSHUser, image.Description)
		}
		return w.Flush()
	}})
	root.AddCommand(parent)
}

func instanceTable(cmd *cobra.Command, instances []cloud.Instance) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tIMAGE\tMACHINE_TYPE\tIP\tCREATED")
	for _, i := range instances {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", i.Name, i.Status, i.Image, i.MachineType, i.IP, i.CreatedAt.Local().Format(time.RFC3339))
	}
	return w.Flush()
}

// instanceAction runs one asynchronous lifecycle request and reports its outcome.
func instanceAction(use, done string, request func(*api.Client, *cobra.Command, string) (cloud.Operation, error)) *cobra.Command {
	var async bool
	command := &cobra.Command{Use: use + " NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := client(cmd)
		op, err := request(c, cmd, args[0])
		if err != nil {
			return err
		}
		if async {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), op.ID)
			return err
		}
		if err := waitOperation(cmd, c, op.ID); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s instance %s\n", done, args[0])
		return err
	}}
	command.Flags().BoolVar(&async, "async", false, "Return the operation ID immediately")
	return command
}

func addInstanceCommands(root *cobra.Command) *cobra.Command {
	parent := &cobra.Command{Use: "instances", Short: "Manage standalone Linux instances"}
	root.AddCommand(parent)
	var image, kind string
	var async bool
	create := &cobra.Command{Use: "create NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := client(cmd)
		op, err := c.CreateInstance(cmd.Context(), args[0], image, kind)
		if err != nil {
			return err
		}
		if async {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), op.ID)
			return err
		}
		if err := waitOperation(cmd, c, op.ID); err != nil {
			return err
		}
		created, err := c.Instance(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created instance %s at %s\n", created.Name, created.IP)
		return err
	}}
	create.Flags().StringVar(&image, "image", "debian-12", "Image from cloud images list")
	create.Flags().StringVar(&kind, "machine-type", "lab-small", "Machine type")
	create.Flags().BoolVar(&async, "async", false, "Return the operation ID immediately")
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		items, err := client(cmd).Instances(cmd.Context())
		if err != nil {
			return err
		}
		return instanceTable(cmd, items)
	}}
	describe := &cobra.Command{Use: "describe NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		item, err := client(cmd).Instance(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return instanceTable(cmd, []cloud.Instance{item})
	}}
	start := instanceAction("start", "Started", func(c *api.Client, cmd *cobra.Command, name string) (cloud.Operation, error) {
		return c.StartInstance(cmd.Context(), name)
	})
	stop := instanceAction("stop", "Stopped", func(c *api.Client, cmd *cobra.Command, name string) (cloud.Operation, error) {
		return c.StopInstance(cmd.Context(), name)
	})
	remove := instanceAction("delete", "Deleted", func(c *api.Client, cmd *cobra.Command, name string) (cloud.Operation, error) {
		return c.DeleteInstance(cmd.Context(), name)
	})
	parent.AddCommand(create, list, describe, start, stop, remove, sshCommand())
	return parent
}
