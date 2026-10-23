package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"nixmox/internal/domain"
)

// Control runs at admission, not on the worker: cancellation must be able to
// interrupt the task currently occupying that worker. Receipt and intended state
// share an atomic journal record, making redelivery safe after a crash.
func (s *Scheduler) Control(ctx context.Context, e domain.Envelope) (domain.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p domain.TaskTarget
	if err := json.Unmarshal(e.Command.Payload, &p); err != nil {
		return domain.Receipt{}, err
	}
	active := s.active[p.TaskID]
	if active != nil {
		active.mu.Lock()
		defer active.mu.Unlock()
	}
	j := s.Store
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Receipt{}, err
	}
	if prior, err := j.admission(e.Command.ID); err == nil {
		if prior.Record.Envelope.RequestHash != e.RequestHash {
			return domain.Receipt{}, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Command ID already used"}
		}
		if err = j.ensureTask(prior); err != nil {
			return domain.Receipt{}, err
		}
		if prior.Control && active != nil && prior.Record.Error == nil {
			if active.current.Version < prior.Initial.Version {
				active.current = prior.Initial
			}
			if active.current.Cancellation.Requested {
				active.cancel()
			}
		}
		if prior.Record.Error != nil {
			return prior.Record.Receipt, prior.Record.Error
		}
		return prior.Record.Receipt, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.Receipt{}, err
	}
	var old domain.Task
	if err := j.read(taskPath(p.TaskID), &old); err != nil {
		return domain.Receipt{}, err
	}
	reject := func(code, message string) (domain.Receipt, error) {
		detail := &domain.Error{Code: code, Message: message}
		receipt := domain.Receipt{CommandID: e.Command.ID, Admission: "rejected"}
		record := domain.AdmissionRecord{SchemaVersion: 1, Envelope: e, Receipt: receipt, Error: detail}
		if err := j.write(receiptPath(e.Command.ID), admissionFile{Record: record}); err != nil {
			return receipt, err
		}
		return receipt, detail
	}
	if old.OwnerNodeID != s.Node || e.OwnerNodeID != s.Node || old.Scope != e.Scope || old.MembershipEpoch != e.MembershipEpoch || (old.ClusterID == nil) != (e.ClusterID == nil) || (old.ClusterID != nil && *old.ClusterID != *e.ClusterID) {
		return reject("INVALID_INPUT", "Task ownership or scope does not match")
	}
	if p.TaskID == "" || e.Command.TargetID != p.TaskID || len(e.Command.ExpectedVersions) != 1 || e.Command.ExpectedVersions["task:"+string(p.TaskID)] == 0 {
		return reject("INVALID_INPUT", "Task ID and exactly one positive task version are required")
	}
	if old.Version != e.Command.ExpectedVersions["task:"+string(p.TaskID)] {
		return reject("VERSION_CONFLICT", "Task changed; review its current lifecycle version")
	}
	if time.Now().After(e.ExpiresAt) {
		return reject("COMMAND_EXPIRED", "Command expired before admission")
	}
	next := old
	cancelling := e.Command.Type == "task.cancel"
	if cancelling {
		if !old.Cancellation.Allowed || terminal(old.State) || (active == nil && old.State != domain.Queued && old.State != domain.Blocked) {
			message := "Task cannot safely be cancelled"
			if old.Cancellation.Reason != nil {
				message = *old.Cancellation.Reason
			}
			return reject("CANCELLATION_REFUSED", message)
		}
		next.Version++
		next.Cancellation.Requested = true
		next.State = domain.Cancelling
		if active == nil {
			next.State = domain.Cancelled
			next.Cancellation.Allowed = false
		}
	} else {
		if e.Command.Type != "task.retry" {
			return reject("INVALID_INPUT", "Unknown task control")
		}
		if !terminal(old.State) {
			return reject("INVALID_INPUT", "Only terminal tasks can be retried")
		}
		definition, ok := s.Definitions[old.Kind]
		if !ok {
			return reject("UNSUPPORTED_CAPABILITY", "Retry handler is unavailable")
		}
		if !definition.SafeRetry {
			return reject("CONFIRMATION_REQUIRED", "This operation requires a fresh operation-specific review before retry")
		}
		next = domain.Task{
			Record:    domain.Record{SchemaVersion: 1, ID: TaskID(e.Command.ID), Version: 1},
			CommandID: e.Command.ID, Kind: old.Kind, State: domain.Queued,
			Scope: old.Scope, OwnerNodeID: old.OwnerNodeID, ClusterID: old.ClusterID,
			MembershipEpoch: old.MembershipEpoch, TargetID: old.TargetID,
			RetryOf: &old.ID, ParentID: old.ParentID, Source: old.Source,
			CreatedAt: time.Now().UTC(), Cancellation: domain.Cancellation{Allowed: true},
			Effects: map[string]bool{}, Log: domain.LogReference{NodeID: s.Node},
		}
	}
	next.UpdatedAt = time.Now().UTC()
	receipt := domain.Receipt{CommandID: e.Command.ID, Admission: "accepted", TaskID: &next.ID}
	file := admissionFile{Record: domain.AdmissionRecord{SchemaVersion: 1, Envelope: e, Receipt: receipt}, Initial: next, Control: cancelling}
	if err := j.write(receiptPath(e.Command.ID), file); err != nil {
		return receipt, err
	}
	if cancelling && active != nil {
		active.current = next
		active.cancel()
	}
	return receipt, j.ensureTask(file)
}
