package cloud

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"time"
)

const (
	groupControllerInterval = 2 * time.Second
	groupRetryDelay         = 15 * time.Second
)

// RunInstanceGroups reconciles persisted group intent until the daemon stops.
func (s *Service) RunInstanceGroups(ctx context.Context) {
	for ctx.Err() == nil {
		s.mu.Lock()
		active := s.state.Autoscaling == "active" || s.state.Autoscaling == "removing" && len(s.state.Groups) > 0
		changed := s.changed
		s.mu.Unlock()
		if !active {
			select {
			case <-ctx.Done():
			case <-changed:
			}
			continue
		}
		if s.groupProxy != "" {
			if err := s.observeGroupRouting(ctx); err != nil && ctx.Err() == nil {
				s.logger.Error("observe instance group routing", "error", err)
			}
		}
		s.reconcileGroups(ctx)
		if err := s.publishGroupPolicies(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("publish instance group policies", "error", err)
		}
		timer := time.NewTimer(groupControllerInterval)
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-changed:
		}
		timer.Stop()
	}
}

func (s *Service) reconcileGroups(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Autoscaling == "" {
		return
	}
	if s.groupProxy != "" {
		if err := s.writeGroupRoutesLocked(); err != nil {
			s.logger.Error("publish instance group routing", "error", err)
			return
		}
	}
	for _, group := range slices.Clone(s.state.Groups) {
		if err := s.reconcileGroup(context.WithoutCancel(ctx), group); err != nil {
			s.logger.Error("reconcile instance group", "group", group.Name, "error", err)
		}
	}
}

func (s *Service) reconcileGroup(ctx context.Context, group Group) error {
	if time.Now().Before(group.RetryAt) {
		return nil
	}
	var members []Instance
	for _, instance := range s.state.Instances {
		if instance.GroupID == group.ID {
			if s.state.instanceBusy(instance.Name) {
				return nil
			}
			members = append(members, instance)
		}
	}
	if group.Deleting && len(members) == 0 {
		next := s.state.clone()
		next.Groups = slices.DeleteFunc(next.Groups, func(g Group) bool { return g.ID == group.ID })
		return s.store(next)
	}
	for _, member := range slices.Backward(members) {
		if group.Deleting || member.Draining || member.Status == "DELETING" || member.Status == "ERROR" || member.Status == "STOPPED" || groupMemberFailed(member) {
			return s.deleteGroupMember(ctx, member)
		}
	}
	if len(members) > group.Desired {
		return s.deleteGroupMember(ctx, members[len(members)-1])
	}
	if len(members) == group.Desired {
		return nil
	}
	var id [8]byte
	rand.Read(id[:])
	name := group.ID + "-" + hex.EncodeToString(id[:])
	if _, err := s.createInstanceLocked(ctx, name, group.Image, group.MachineType, "cloud", group.ID, group.Bootstrap); err != nil {
		next := s.state.clone()
		recordGroupError(&next, group.ID, err)
		return s.store(next)
	}
	return nil
}

func recordGroupError(st *snapshot, key string, err error) {
	g, lookup := st.group(key)
	if lookup != nil {
		return
	}
	g.LastError = ""
	g.RetryAt = time.Time{}
	if err != nil {
		g.LastError = err.Error()
		g.RetryAt = time.Now().UTC().Add(groupRetryDelay)
	}
}

func (s *Service) deleteGroupMember(ctx context.Context, instance Instance) error {
	next := s.state.clone()
	member, err := next.instance(instance.Name)
	if err != nil {
		return err
	}
	if s.groupProxy != "" && !member.ReadyAt.IsZero() {
		if !member.Draining {
			member.Draining = true
			return s.store(next)
		}
		if member.DrainUntil.IsZero() || time.Now().Before(member.DrainUntil) {
			return nil
		}
	}
	member.Status = "DELETING"
	op := next.newOperation("DELETE_INSTANCE", instance.Name, "cloud")
	if err := s.store(next); err != nil {
		return err
	}
	s.launch(op.ID, func() error {
		if err := s.instances.DeleteOwnedInstance(ctx, instance.Name, instance.GroupID); err != nil {
			return fmt.Errorf("remove group member %s: %w", instance.Name, err)
		}
		return os.RemoveAll(s.instanceDir(instance.Name))
	}, func(st *snapshot, err error) {
		recordGroupError(st, instance.GroupID, err)
		if err == nil {
			st.Instances = slices.DeleteFunc(st.Instances, func(i Instance) bool { return i.Name == instance.Name && i.GroupID == instance.GroupID })
		}
	})
	return nil
}
