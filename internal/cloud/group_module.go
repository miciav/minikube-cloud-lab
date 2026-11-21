package cloud

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// InstanceAutoscalingStatus describes module state and resources still to remove.
type InstanceAutoscalingStatus struct {
	State     string   `json:"state"`
	Remaining []string `json:"remaining"`
}

// PrepareInstanceAutoscaling publishes the initial files before Compose starts the optional services.
func (s *Service) PrepareInstanceAutoscaling() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groupProxy == "" || s.groupAutoscaler == "" || s.groupMetrics == "" || s.instances == nil {
		return fmt.Errorf("%w: instance autoscaling requires the lab Compose configuration", ErrFailedPrecondition)
	}
	if s.state.Autoscaling == "removing" {
		return fmt.Errorf("%w: finish uninstalling instance autoscaling first", ErrFailedPrecondition)
	}
	if err := s.writeGroupRoutesLocked(); err != nil {
		return err
	}
	if _, err := s.writeGroupPoliciesLocked(); err != nil {
		return err
	}
	if s.state.Autoscaling == "" {
		next := s.state.clone()
		next.Autoscaling = "installing"
		return s.store(next)
	}
	return nil
}

// ActivateReadyInstanceAutoscaling enables the controller after its optional services start.
func (s *Service) ActivateReadyInstanceAutoscaling(ctx context.Context) error {
	if s.groupProxy == "" || s.groupMetrics == "" || s.groupAutoscaler == "" {
		return fmt.Errorf("%w: instance autoscaling requires the lab Compose configuration", ErrFailedPrecondition)
	}
	for _, endpoint := range []string{s.groupProxy + "/api/version", s.groupMetrics + "/-/ready", s.groupAutoscaler + "/v1/health"} {
		res, err := groupGet(ctx, endpoint)
		if err != nil {
			return fmt.Errorf("%w: wait for %s: %w", ErrUnavailable, endpoint, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("%w: %s returned %s", ErrUnavailable, endpoint, res.Status)
		}
	}
	return s.ActivateInstanceAutoscaling()
}

// BeginInstanceAutoscalingRemoval blocks changes and persists deletion intent for every group.
func (s *Service) BeginInstanceAutoscalingRemoval() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Autoscaling == "removing" || s.state.Autoscaling == "" {
		return nil
	}
	next := s.state.clone()
	next.Autoscaling = "removing"
	for i := range next.Groups {
		next.Groups[i].Deleting = true
		next.Groups[i].RetryAt = time.Time{}
	}
	return s.store(next)
}

// InstanceAutoscaling reports progress while the controller removes group members.
func (s *Service) InstanceAutoscaling() InstanceAutoscalingStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := InstanceAutoscalingStatus{State: s.state.Autoscaling, Remaining: []string{}}
	if status.State == "" {
		status.State = "disabled"
	}
	for _, group := range s.state.Groups {
		item := group.Name
		if group.LastError != "" {
			item += " (" + group.LastError + ")"
		}
		status.Remaining = append(status.Remaining, item)
	}
	for _, member := range s.state.Instances {
		if member.GroupID != "" {
			status.Remaining = append(status.Remaining, member.Name)
		}
	}
	return status
}

// FinishInstanceAutoscalingRemoval clears the module state after Compose stops its services.
func (s *Service) FinishInstanceAutoscalingRemoval() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Autoscaling == "active" || s.state.Autoscaling == "installing" || len(s.state.Groups) > 0 {
		return fmt.Errorf("%w: instance group cleanup is still pending", ErrFailedPrecondition)
	}
	for _, member := range s.state.Instances {
		if member.GroupID != "" {
			return fmt.Errorf("%w: group member %s remains", ErrFailedPrecondition, member.Name)
		}
	}
	for _, dir := range []string{"traefik", "policies"} {
		if err := os.RemoveAll(filepath.Join(s.groupFiles(), dir)); err != nil {
			return err
		}
	}
	next := s.state.clone()
	next.Autoscaling = ""
	return s.store(next)
}
