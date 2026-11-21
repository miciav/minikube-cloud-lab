package cli

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func groupTable(cmd *cobra.Command, groups []cloud.Group) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tID\tMIN\tDESIRED\tMAX\tTEMPLATE\tREADY\tPENDING\tSTATUS\tERROR")
	for _, g := range groups {
		status := "ACTIVE"
		if g.Deleting {
			status = "DELETING"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n", g.Name, g.ID, g.Min, g.Desired, g.Max, g.TemplateVersion, g.Ready, g.Pending, status, g.LastError)
	}
	return w.Flush()
}

func addGroupCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "groups", Short: "Manage autoscaling groups of standalone instances"}
	root.AddCommand(parent)

	var spec cloud.GroupSpec
	var bootstrap string
	create := &cobra.Command{Use: "create NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		reader := cmd.InOrStdin()
		if bootstrap != "-" {
			file, err := os.Open(bootstrap) // #nosec G304 -- explicitly supplied by the CLI user.
			if err != nil {
				return err
			}
			defer func() { _ = file.Close() }()
			reader = file
		}
		script, err := io.ReadAll(io.LimitReader(reader, cloud.MaxGroupBootstrapBytes+1))
		if err != nil {
			return err
		}
		spec.Name, spec.Bootstrap = args[0], string(script)
		group, err := client(cmd).CreateGroup(cmd.Context(), spec)
		if err != nil {
			return err
		}
		return groupTable(cmd, []cloud.Group{group})
	}}
	create.Flags().StringVar(&spec.Image, "image", "ubuntu-26.04", "Instance image")
	create.Flags().StringVar(&spec.MachineType, "machine-type", "lab-small", "Machine type")
	create.Flags().StringVar(&bootstrap, "bootstrap", "", "Bootstrap script file or - for stdin")
	create.Flags().IntVar(&spec.Min, "min", 1, "Minimum capacity")
	create.Flags().IntVar(&spec.Desired, "desired", 1, "Initial desired capacity")
	create.Flags().IntVar(&spec.Max, "max", 3, "Maximum capacity")
	create.Flags().IntVar(&spec.DrainSeconds, "drain-seconds", 10, "Seconds allowed for in-flight HTTP requests after deregistration")
	create.Flags().IntVar(&spec.Port, "port", 8080, "Application HTTP port")
	create.Flags().StringVar(&spec.HealthPath, "health-path", "/health", "HTTP health check path")
	create.Flags().Float64Var(&spec.TargetRPS, "target-rps", 40, "Target requests per second per healthy instance")
	_ = create.MarkFlagRequired("bootstrap")

	var desired int
	resize := &cobra.Command{Use: "resize NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		group, err := client(cmd).ResizeGroup(cmd.Context(), args[0], desired)
		if err != nil {
			return err
		}
		return groupTable(cmd, []cloud.Group{group})
	}}
	resize.Flags().IntVar(&desired, "desired", 0, "Desired capacity")
	_ = resize.MarkFlagRequired("desired")

	parent.AddCommand(create, resize,
		&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			groups, err := client(cmd).Groups(cmd.Context())
			if err != nil {
				return err
			}
			return groupTable(cmd, groups)
		}},
		&cobra.Command{Use: "describe NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			group, err := client(cmd).Group(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return groupTable(cmd, []cloud.Group{group})
		}},
		&cobra.Command{Use: "delete NAME", Short: "Delete a group and its instances, including their local filesystems", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			return client(cmd).DeleteGroup(cmd.Context(), args[0])
		}},
	)
}
