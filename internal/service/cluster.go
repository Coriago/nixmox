package service

import (
	"context"
	"encoding/json"
	"time"

	"nixmox/internal/domain"
)

type ClusterTransport interface {
	Submit(domain.Command) (domain.Receipt, error)
	Receipt(string) (domain.Receipt, error)
}

func (s *Service) Receipt(id string) (domain.Receipt, error) {
	s.mu.Lock()
	a, ok := s.admissions[id]
	transport := s.clusterTransport
	s.mu.Unlock()
	if transport != nil && (!ok || a.Scope == "cluster") {
		return transport.Receipt(id)
	}
	return s.LocalReceipt(id)
}

func (s *Service) SetClusterTransport(transport ClusterTransport) {
	s.mu.Lock()
	s.clusterTransport = transport
	s.mu.Unlock()
}
func (s *Service) Admit(c domain.Command) (domain.Receipt, error) {
	c, fingerprint, err := domain.CanonicalCommand(c)
	if err != nil {
		return domain.Receipt{}, err
	}
	s.mu.Lock()
	if a, ok := s.admissions[c.ID]; ok && a.Fingerprint != fingerprint {
		s.mu.Unlock()
		return domain.Receipt{}, domain.Err("IDEMPOTENCY_CONFLICT", "Command ID is already bound to another request.", 409)
	}
	op, ok := s.operations[c.Type]
	cluster := ok && op.Scope == "cluster"
	if c.Type == "task.cancel" || c.Type == "task.retry" {
		var p struct {
			TaskID string `json:"task_id"`
		}
		if domain.StrictJSON(c.Payload, &p) == nil {
			if task, ok := s.tasks[p.TaskID]; ok {
				cluster = task.Scope == "cluster"
			}
		}
	}
	transport := s.clusterTransport
	s.mu.Unlock()
	if cluster {
		if transport == nil {
			return domain.Receipt{}, domain.Err("QUORUM_UNAVAILABLE", "Cluster admission is unavailable.", 503)
		}
		return transport.Submit(c)
	}
	return s.admit(c, nil)
}
func (s *Service) AdmitDelivered(prepared domain.PreparedCommand) (domain.Receipt, error) {
	return s.admit(prepared.Command, &prepared)
}
func (s *Service) ValidateCluster(c domain.Command) error {
	if c.Type == "task.cancel" || c.Type == "task.retry" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.identity.ClusterID == nil {
			return domain.Err("QUORUM_UNAVAILABLE", "Cluster admission is unavailable.", 503)
		}
		_, err := s.prepareClusterControl(c, "", *s.identity.ClusterID)
		return err
	}
	op, ok := s.operations[c.Type]
	if !ok || op.Scope != "cluster" {
		return domain.Err("UNSUPPORTED_CAPABILITY", "Operation is unavailable for cluster admission.", 503)
	}
	return op.Validate(c)
}

// PrepareCluster validates and pins selectors before the transport completes its
// durable intent. Delivery reuses these values, never a fresh selector resolution.
func (s *Service) PrepareCluster(ctx context.Context, c domain.Command, cluster string) (domain.PreparedCommand, error) {
	c, fingerprint, err := domain.CanonicalCommand(c)
	if err != nil {
		return domain.PreparedCommand{}, err
	}
	if c.Type == "task.cancel" || c.Type == "task.retry" {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.prepareClusterControl(c, fingerprint, cluster)
	}
	op, ok := s.operations[c.Type]
	if !ok || op.Scope != "cluster" {
		return domain.PreparedCommand{}, domain.Err("UNSUPPORTED_CAPABILITY", "Operation is unavailable for cluster admission.", 503)
	}
	if err := op.Validate(c); err != nil {
		return domain.PreparedCommand{}, err
	}
	now := time.Now().UTC()
	p := domain.PreparedCommand{Command: c, ClusterID: cluster, Fingerprint: fingerprint, Kind: c.Type, Resolved: c.Payload, AdmittedAt: now, ExpiresAt: now.Add(120 * time.Second)}
	if op.Resolve != nil {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		p.Resolved, err = op.Resolve(ctx, c)
		if err != nil {
			return p, err
		}
	}
	if len(p.Resolved) > domain.MaxCommandBytes || !json.Valid(p.Resolved) {
		return p, domain.Err("INVALID_STATE", "Resolved request exceeds encoding bounds.", 422)
	}
	if op.Exclusions != nil {
		p.Exclusions, err = op.Exclusions(c, p.Resolved)
		if err != nil {
			return p, err
		}
		if err = validateExclusions(p.Exclusions); err != nil {
			return p, err
		}
	}
	return p, nil
}
func (s *Service) prepareClusterControl(c domain.Command, fingerprint, cluster string) (domain.PreparedCommand, error) {
	var payload struct {
		TaskID string `json:"task_id"`
	}
	if domain.StrictJSON(c.Payload, &payload) != nil || !domain.SafeID.MatchString(payload.TaskID) {
		return domain.PreparedCommand{}, domain.Err("INVALID_PAYLOAD", "A task_id is required.", 422)
	}
	t, ok := s.tasks[payload.TaskID]
	if !ok {
		return domain.PreparedCommand{}, domain.Err("NOT_FOUND", "Task not found.", 404)
	}
	if t.Scope != "cluster" || c.TargetID != t.TargetID || t.ClusterID == nil || *t.ClusterID != cluster {
		return domain.PreparedCommand{}, domain.Err("INVALID_COMMAND", "Task control scope or target differs from its owner.", 422)
	}
	if len(c.ExpectedVersions) != 1 || c.ExpectedVersions["task:"+t.ID] != t.Version {
		return domain.PreparedCommand{}, domain.Err("VERSION_CONFLICT", "Task changed after review.", 409)
	}
	if c.Type == "task.cancel" && (t.Terminal() || !t.Cancellation.Allowed) {
		return domain.PreparedCommand{}, domain.Err("INVALID_STATE", "Cancellation is unavailable in this phase.", 409)
	}
	if c.Type == "task.retry" {
		if !t.Terminal() {
			return domain.PreparedCommand{}, domain.Err("INVALID_STATE", "Only terminal tasks can be retried.", 409)
		}
		if !s.operations[t.Kind].SafeRetry {
			return domain.PreparedCommand{}, domain.Err("CONFIRMATION_REQUIRED", "This operation requires a fresh review.", 409)
		}
	}
	a := s.admissions[t.CommandID]
	now := time.Now().UTC()
	return domain.PreparedCommand{Command: c, ClusterID: cluster, Fingerprint: fingerprint, Kind: t.Kind, Resolved: a.Resolved, Exclusions: append([]string(nil), a.Exclusions...), AdmittedAt: now, ExpiresAt: now.Add(120 * time.Second)}, nil
}
