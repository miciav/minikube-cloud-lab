package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func clusterTable(cmd *cobra.Command, clusters []cloud.Cluster) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tMACHINE_TYPE\tMIN\tMAX\tCREATED")
	for _, c := range clusters {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n", c.Name, c.Status, c.MachineType, c.MinMachines, c.MaxMachines, c.CreatedAt.Local().Format(time.RFC3339))
	}
	return w.Flush()
}

func addClusterCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "clusters", Short: "Manage Kubernetes clusters"}
	root.AddCommand(parent)
	var kind string
	var minM, maxM int
	var asyncCreate bool
	create := &cobra.Command{Use: "create NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := client(cmd)
		op, err := c.CreateCluster(cmd.Context(), args[0], kind, minM, maxM)
		if err != nil {
			return err
		}
		if asyncCreate {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), op.ID)
			return err
		}
		if err := waitOperation(cmd, c, op.ID); err != nil {
			return err
		}
		plural := "machines"
		if minM == 1 {
			plural = "machine"
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created cluster %s (%d %s)\n", args[0], minM, plural)
		return err
	}}
	create.Flags().StringVar(&kind, "machine-type", "lab-small", "Machine type")
	create.Flags().IntVar(&minM, "min-nodes", 1, "Minimum machines including the control plane")
	create.Flags().IntVar(&maxM, "max-nodes", 3, "Maximum machines including the control plane")
	create.Flags().BoolVar(&asyncCreate, "async", false, "Return the operation ID immediately")
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		items, err := client(cmd).Clusters(cmd.Context())
		if err != nil {
			return err
		}
		return clusterTable(cmd, items)
	}}
	describe := &cobra.Command{Use: "describe NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		item, err := client(cmd).Cluster(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return clusterTable(cmd, []cloud.Cluster{item})
	}}
	var asyncDelete bool
	remove := &cobra.Command{Use: "delete NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := client(cmd)
		op, err := c.DeleteCluster(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if asyncDelete {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), op.ID)
			return err
		}
		if err := waitOperation(cmd, c, op.ID); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted cluster %s\n", args[0])
		return err
	}}
	remove.Flags().BoolVar(&asyncDelete, "async", false, "Return the operation ID immediately")
	credentials := &cobra.Command{Use: "get-credentials NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		data, err := client(cmd).Kubeconfig(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if err := mergeCredentials(defaultKubeconfig(), args[0], data); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Credentials configured for cluster %s\n", args[0])
		return err
	}}
	parent.AddCommand(create, list, describe, remove, credentials)
}
