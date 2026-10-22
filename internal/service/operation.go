package service

import (
	"context"
	"encoding/json"
	"errors"
	"nixmox/internal/domain"
	"time"
)

// Operation is registered by a feature service, never by an HTTP request.
// Validate is pure and repeated on execution. Resolve pins mutable selectors
// before acceptance. Reconcile must inspect uncertain effects before repeating.
type Operation struct {
	Kind             string
	Scope            string
	RunOnCoordinator bool
	Timeout          time.Duration
	Validate         func(domain.Command) error
	Resolve          func(context.Context, domain.Command) (json.RawMessage, error)
	Run              func(context.Context, *Execution) (any, error)
	Reconcile        func(context.Context, *Execution) (any, error)
	SafeRetry        bool
	Exclusions       func(domain.Command, json.RawMessage) ([]string, error)
}
type Execution struct {
	service  *Service
	TaskID   string
	Command  domain.Command
	Resolved json.RawMessage
}

func (e *Execution) Snapshot() (domain.Task, error) { return e.service.Task(e.TaskID) }
func (e *Execution) RefreshIdentity() error {
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.store.Identity()
	if err != nil {
		return err
	}
	s.identity = id
	s.notify()
	return nil
}
func (e *Execution) Checkpoint(phase string, value any, cancellable bool) error {
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[e.TaskID]
	if !ok || t.Terminal() {
		return domain.Err("INVALID_STATE", "Task cannot checkpoint in this state.", 409)
	}
	if t.State == "cancelling" {
		return context.Canceled
	}
	b, err := json.Marshal(value)
	if err != nil || len(b) > domain.MaxCommandBytes {
		return domain.Err("INVALID_STATE", "Checkpoint cannot be encoded within its limit.", 422)
	}
	t.Phase = phase
	t.Checkpoint = b
	t.Cancellation.Allowed = cancellable
	t.Version++
	t.UpdatedAt = time.Now().UTC()
	if err := s.persistTask(t); err != nil {
		return unknown()
	}
	s.notify()
	return nil
}
func (e *Execution) MarkEffect(name string, observed bool) error {
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tasks[e.TaskID]
	if t.Terminal() {
		return domain.Err("INVALID_STATE", "Terminal tasks are immutable.", 409)
	}
	effects := make(map[string]bool, len(t.Effects)+1)
	for k, v := range t.Effects {
		effects[k] = v
	}
	effects[name] = observed
	t.Effects = effects
	t.Version++
	t.UpdatedAt = time.Now().UTC()
	if err := s.persistTask(t); err != nil {
		return unknown()
	}
	s.notify()
	return nil
}

type Blocked struct {
	Phase  string
	Reason *domain.Error
}

func (b *Blocked) Error() string { return b.Reason.Error() }
func Block(phase, code, message string) error {
	return &Blocked{Phase: phase, Reason: domain.Err(code, message, 409)}
}
func networkOperation(inspector Inspector) Operation {
	run := func(ctx context.Context, e *Execution) (any, error) {
		status, err := inspector.Inspect(ctx, e.Command.TargetID)
		if err != nil {
			return nil, domain.Err("NETWORK_UNAVAILABLE", "Local network inspection failed.", 503)
		}
		return status, nil
	}
	return Operation{Kind: "network.check", Scope: "node_local", Timeout: 5 * time.Second, SafeRetry: true, Validate: func(c domain.Command) error {
		var empty struct{}
		if len(c.ExpectedVersions) != 0 || domain.StrictJSON(c.Payload, &empty) != nil {
			return domain.Err("INVALID_PAYLOAD", "network.check accepts an empty payload and no expected versions.", 422)
		}
		return nil
	}, Run: run, Reconcile: run}
}
func (s *Service) Operation(kind string) (Operation, bool) {
	op, ok := s.operations[kind]
	return op, ok
}
func (s *Service) runOperation(ctx context.Context, t domain.Task, a admission) (any, error) {
	op, ok := s.operations[t.Kind]
	if !ok {
		return nil, domain.Err("UNSUPPORTED_CAPABILITY", "Operation is unavailable on this executor.", 503)
	}
	c := a.Command
	s.mu.Lock()
	for depth := 0; c.Type == "task.retry" && depth < domain.MaxRecords; depth++ {
		var p struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal(c.Payload, &p) != nil {
			s.mu.Unlock()
			return nil, domain.Err("UNAVAILABLE", "Retry source is unavailable.", 503)
		}
		parent, ok := s.tasks[p.TaskID]
		if !ok {
			s.mu.Unlock()
			return nil, domain.Err("UNAVAILABLE", "Retry source is unavailable.", 503)
		}
		original, ok := s.admissions[parent.CommandID]
		if !ok {
			s.mu.Unlock()
			return nil, domain.Err("UNAVAILABLE", "Retry source is unavailable.", 503)
		}
		c = original.Command
	}
	s.mu.Unlock()
	if err := op.Validate(c); err != nil {
		return nil, err
	}
	e := &Execution{service: s, TaskID: t.ID, Command: c, Resolved: a.Resolved}
	if a.Reconcile {
		if op.Reconcile == nil {
			return nil, Block("reconciling", "RECONCILIATION_REQUIRED", "The executor must inspect uncertain effects before continuing.")
		}
		return op.Reconcile(ctx, e)
	}
	return op.Run(ctx, e)
}
func safeOperationError(err error) *domain.Error {
	var e *domain.Error
	if errors.As(err, &e) {
		return e
	}
	return domain.Err("OPERATION_FAILED", "The operation failed; inspect its sanitized diagnostics.", 500)
}
