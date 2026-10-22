package service

import (
	"nixmox/internal/domain"
	"time"
)

func (e *Execution) SetApproval(approval domain.DeploymentApproval) error {
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[e.TaskID]
	if !ok || t.Terminal() {
		return domain.Err("INVALID_STATE", "Task cannot record approval.", 409)
	}
	t.Approval = &approval
	t.Version++
	t.UpdatedAt = time.Now().UTC()
	if err := s.persistTask(t); err != nil {
		return unknown()
	}
	s.notify()
	return nil
}
func (e *Execution) ReadTask(id string) (domain.Task, error) { return e.service.LocalTask(id) }

// Approval and resumption share one durable task write. A process failure can
// never leave approval recorded while silently losing the resume request.
func (e *Execution) ApproveDeployment(id string, diff domain.PackageDiff) error {
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return domain.Err("NOT_FOUND", "Deployment task is unavailable on this executor.", 404)
	}
	a := t.Approval
	if a == nil || a.DiffID != diff.ID || a.DiffVersion != diff.Version || a.CandidateID != diff.CandidateID || a.TargetID != diff.HostID || t.TargetID != e.Command.TargetID || diff.State != "ready" || diff.Stale {
		return domain.Err("APPROVAL_STALE", "Approval does not match the exact candidate, baseline and target.", 409)
	}
	if a.State == "approved" && a.CommandID == e.Command.ID {
		return nil
	}
	if t.State != "blocked" || t.Phase != "review" || a.State != "pending" || a.AutoApprove {
		return domain.Err("INVALID_STATE", "Deployment is not awaiting this approval.", 409)
	}
	next := *a
	next.State = "approved"
	next.Method = "manual"
	next.CommandID = e.Command.ID
	t.Approval = &next
	if err := t.Transition("reconciling", t.Phase); err != nil {
		return err
	}
	t.Error = nil
	if err := s.persistTask(t); err != nil {
		return unknown()
	}
	s.notify()
	s.signal()
	return nil
}
