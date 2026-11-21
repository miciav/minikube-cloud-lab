package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/miciav/minikube-cloud-lab/internal/api"
	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func instanceAutoscalingCommand() *cobra.Command {
	root := &cobra.Command{Use: "instance-autoscaling", Hidden: true}
	for _, action := range []string{"prepare", "activate", "begin-removal", "finish-removal"} {
		root.AddCommand(&cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			c := client(cmd)
			if action == "activate" {
				return waitAutoscalingActivation(cmd.Context(), c)
			}
			result, err := c.InstanceAutoscaling(cmd.Context(), action)
			if err != nil || action != "begin-removal" {
				return err
			}
			return waitAutoscalingRemoval(cmd.Context(), result.Remaining, c.InstanceAutoscaling)
		}})
	}
	return root
}

func waitAutoscalingRemoval(ctx context.Context, remaining []string, status func(context.Context, string) (cloud.InstanceAutoscalingStatus, error)) error {
	const stallTimeout = 5 * time.Minute
	deadline := time.Now().Add(stallTimeout)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for len(remaining) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		result, err := status(ctx, "")
		if err != nil {
			return err
		}
		if len(result.Remaining) < len(remaining) {
			deadline = time.Now().Add(stallTimeout)
		} else if !time.Now().Before(deadline) {
			return fmt.Errorf("instance group cleanup stalled for %s", strings.Join(result.Remaining, ", "))
		}
		remaining = result.Remaining
	}
	return nil
}

func waitAutoscalingActivation(parent context.Context, c *api.Client) error {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		_, err := c.InstanceAutoscaling(ctx, "activate")
		var response *api.Error
		if !errors.As(err, &response) || response.Status != http.StatusServiceUnavailable {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("instance autoscaling services failed readiness checks: %w", err)
		case <-tick.C:
		}
	}
}
