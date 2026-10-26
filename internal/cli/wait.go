package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/miciav/minikube-cloud-lab/internal/api"
)

func waitOperation(cmd *cobra.Command, c *api.Client, id string) error {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for operation %s...\n", id)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	interrupted := func() error {
		return fmt.Errorf("still running: cloud operations describe %s: %w", id, cmd.Context().Err())
	}
	for {
		op, err := c.Operation(cmd.Context(), id)
		if err != nil {
			if cmd.Context().Err() != nil {
				return interrupted()
			}
			return err
		}
		switch op.Status {
		case "DONE":
			return nil
		case "ERROR":
			return fmt.Errorf("operation %s failed: %s", id, op.Error)
		}
		select {
		case <-cmd.Context().Done():
			return interrupted()
		case <-ticker.C:
		}
	}
}
