package cloud

import (
	"fmt"
	"math"
	"time"
)

// ResizeCluster grows a lab and fixes its minimum and maximum at nodes.
func (s *Service) ResizeCluster(name string, nodes int, requester string) ([]Operation, error) {
	return s.ResizeClusterIfUnchanged(name, nodes, requester, nil)
}

// ResizeClusterIfUnchanged never deletes machines or changes cluster identity.
func (s *Service) ResizeClusterIfUnchanged(name string, nodes int, requester string, expected *time.Time) ([]Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if nodes < 1 || nodes > math.MaxInt32 {
		return nil, fmt.Errorf("%w: require 1 <= nodes <= 2147483647", ErrInvalid)
	}
	c, err := s.ready(name)
	if err != nil {
		return nil, err
	}
	if expected != nil && !c.CreatedAt.Equal(*expected) {
		return nil, fmt.Errorf("%w: cluster changed; refresh before resizing", ErrFailedPrecondition)
	}
	for _, op := range s.state.Operations {
		if op.Status == "RUNNING" {
			return nil, fmt.Errorf("%w: an operation is still running", ErrFailedPrecondition)
		}
	}
	for _, m := range s.state.Machines {
		if m.Status != "RUNNING" {
			return nil, fmt.Errorf("%w: machine %s is %s; reconcile it before resizing", ErrFailedPrecondition, m.Name, m.Status)
		}
	}
	current := len(s.state.Machines)
	if nodes < current {
		return nil, fmt.Errorf("%w: shrinking is not supported; cluster has %d nodes", ErrFailedPrecondition, current)
	}
	if nodes == current && c.MinMachines == nodes && c.MaxMachines == nodes {
		return []Operation{}, nil
	}
	next := s.state.clone()
	updated, _ := next.cluster(name)
	updated.MinMachines, updated.MaxMachines = nodes, nodes
	return s.addMachines(*updated, nodes-current, requester, next)
}
