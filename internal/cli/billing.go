package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func addBillingCommands(root *cobra.Command) {
	var since string
	cmd := &cobra.Command{Use: "billing", Short: "Show usage costs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := client(cmd).Billing(cmd.Context(), since)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Since %s (reset %s)\n", r.Since.Format(time.RFC3339), r.ResetAt.Format(time.RFC3339))
		w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
		fmt.Fprintln(w, "KIND\tNAME\tTYPE/SIZE\tSTATUS\tHOURS\tCOST_EUR")
		for _, i := range r.Items {
			size, status := i.Type, "stopped"
			if size == "" {
				size = fmt.Sprintf("%.1fGi", float64(i.Bytes)/(1<<30))
			}
			if i.Running {
				status = "running"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.2f\t%.4f\n", i.Kind, i.Name, size, status, i.Seconds/3600, i.Cost)
		}
		fmt.Fprintf(w, "TOTAL\t\t\t\t\t%.4f\n", r.Total)
		return w.Flush()
	}}
	cmd.Flags().StringVar(&since, "since", "", "duration (1h) or RFC3339 time; default since the last reset")
	root.AddCommand(cmd)
}
