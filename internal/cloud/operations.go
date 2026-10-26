package cloud

import (
	"context"
	"fmt"
	"sort"
	"time"
)

func (st *snapshot) newOperation(kind, target, requester string) Operation {
	op := Operation{ID: fmt.Sprintf("op-%06d", len(st.Operations)+1), Type: kind, Target: target, Requester: requester, Status: "RUNNING", StartedAt: time.Now().UTC()}
	st.Operations = append(st.Operations, op)
	return op
}

func (st *snapshot) operation(id string) *Operation {
	for i := range st.Operations {
		if st.Operations[i].ID == id {
			return &st.Operations[i]
		}
	}
	return nil
}
func (s *Service) notify() { close(s.changed); s.changed = make(chan struct{}) }
func (s *Service) store(next snapshot) error {
	if err := save(s.dir, next); err != nil {
		return err
	}
	s.state = next
	s.notify()
	return nil
}

func (s *Service) launch(id string, work func() error, finish func(*snapshot, error)) {
	go func() {
		s.workMu.Lock()
		defer s.workMu.Unlock()
		err := work()
		s.mu.Lock()
		defer s.mu.Unlock()
		next := s.state.clone()
		finish(&next, err)
		op := next.operation(id)
		now := time.Now().UTC()
		op.EndedAt = &now
		op.Status = "DONE"
		if err != nil {
			op.Status = "ERROR"
			op.Error = err.Error()
		}
		if err := s.store(next); err != nil {
			op.Status = "ERROR"
			op.Error = fmt.Sprintf("cannot persist outcome: %v", err)
			s.state = next
			s.notify()
			s.logger.Error("persist operation outcome", "error", err)
		}
		s.logger.Info("operation finished", "id", op.ID, "requester", op.Requester, "type", op.Type, "status", op.Status, "error", op.Error, "duration", now.Sub(op.StartedAt))
	}()
}

// Operations returns newest requests first.
func (s *Service) Operations() []Operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	ops := append([]Operation{}, s.state.Operations...)
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].StartedAt.After(ops[j].StartedAt) })
	return ops
}

// Operation returns a request from the registry.
func (s *Service) Operation(id string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.operation(id)
}

func (s *Service) operation(id string) (Operation, error) {
	op := s.state.operation(id)
	if op == nil {
		return Operation{}, fmt.Errorf("%w: operation %s", ErrNotFound, id)
	}
	return *op, nil
}

// Wait observes an operation without canceling its work.
func (s *Service) Wait(ctx context.Context, id string) (Operation, error) {
	for {
		s.mu.Lock()
		op, err := s.operation(id)
		changed := s.changed
		s.mu.Unlock()
		if err != nil || op.Status != "RUNNING" {
			return op, err
		}
		select {
		case <-ctx.Done():
			return op, ctx.Err()
		case <-changed:
		}
	}
}
