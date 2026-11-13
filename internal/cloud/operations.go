package cloud

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// maxFinishedOperations bounds the persisted history; running operations are always kept.
const maxFinishedOperations = 100

func (st *snapshot) newOperation(kind, target, requester string) Operation {
	next := 1
	if n := len(st.Operations); n > 0 {
		// Operations are appended in order, so the last one holds the highest ID.
		_, _ = fmt.Sscanf(st.Operations[n-1].ID, "op-%d", &next)
		next++
	}
	op := Operation{ID: fmt.Sprintf("op-%06d", next), Type: kind, Target: target, Requester: requester, Status: "RUNNING", StartedAt: time.Now().UTC()}
	finished := 0
	for _, existing := range st.Operations {
		if existing.Status != "RUNNING" {
			finished++
		}
	}
	for i := 0; finished >= maxFinishedOperations && i < len(st.Operations); {
		if st.Operations[i].Status == "RUNNING" {
			i++
			continue
		}
		st.Operations = slices.Delete(st.Operations, i, i+1)
		finished--
	}
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
	accepted := s.state.operation(id)
	s.logger.Info("operation started", "id", id, "requester", accepted.Requester, "type", accepted.Type, "target", accepted.Target)
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
	slices.Reverse(ops) // appended in start order
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
