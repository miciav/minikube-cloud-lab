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
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for len(result.Remaining) > 0 {
				select {
				case <-ctx.Done():
					return fmt.Errorf("instance group cleanup pending for %s: %w", strings.Join(result.Remaining, ", "), ctx.Err())
				case <-tick.C:
					result, err = c.InstanceAutoscaling(ctx, "")
					if err != nil {
						return err
					}
				}
			}
			return nil
		}})
	}
	return root
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
