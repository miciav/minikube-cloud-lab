package cli

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func addRegistryCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "registries", Short: "Manage container registries"}
	create := &cobra.Command{Use: "create NAME", Short: "Create a registry with push and pull credentials", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := client(cmd)
		op, err := c.CreateRegistry(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if err := waitOperation(cmd, c, op.ID); err != nil {
			return err
		}
		registries, err := c.Registries(cmd.Context())
		if err != nil {
			return err
		}
		i := slices.IndexFunc(registries, func(r cloud.Registry) bool { return r.Name == args[0] })
		if i < 0 {
			return fmt.Errorf("registry %s disappeared after creation", args[0])
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created registry %[1]s at %[2]s\nCredentials:\n  cloud registries credentials %[1]s --push\n  cloud registries credentials %[1]s --pull\n", args[0], registries[i].Endpoint)
		return err
	}}
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		registries, err := client(cmd).Registries(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tENDPOINT\tSTATUS")
		for _, r := range registries {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", r.Name, r.Endpoint, r.Status)
		}
		return w.Flush()
	}}
	var push, pull bool
	credentials := &cobra.Command{Use: "credentials NAME --push|--pull", Short: "Print the read/write (push) or read-only (pull) account", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if push == pull {
			return errors.New("choose exactly one of --push or --pull")
		}
		access := "pull"
		if push {
			access = "push"
		}
		credential, err := client(cmd).RegistryCredentials(cmd.Context(), args[0], access)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "endpoint %s\nusername %s\npassword %s\n", credential.Endpoint, credential.Username, credential.Password)
		return err
	}}
	credentials.Flags().BoolVar(&push, "push", false, "Read/write account")
	credentials.Flags().BoolVar(&pull, "pull", false, "Read-only account")
	images := &cobra.Command{Use: "images NAME", Short: "List the images stored in a registry", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		items, err := client(cmd).RegistryImages(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "REPOSITORY\tTAG\tSIZE\tPUSHED")
		for _, image := range items {
			pushed := "-"
			if !image.Pushed.IsZero() {
				pushed = image.Pushed.Local().Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", image.Repository, image.Tag, strconv.FormatInt(image.Bytes, 10), pushed)
		}
		return w.Flush()
	}}
	var force bool
	remove := &cobra.Command{Use: "delete NAME", Short: "Delete a registry and its images", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := client(cmd)
		op, err := c.DeleteRegistry(cmd.Context(), args[0], force)
		if err != nil {
			return err
		}
		if err := waitOperation(cmd, c, op.ID); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted registry %s\n", args[0])
		return err
	}}
	remove.Flags().BoolVar(&force, "force", false, "Delete a registry that still holds images")
	parent.AddCommand(create, list, credentials, images, remove)
	root.AddCommand(parent)
}
