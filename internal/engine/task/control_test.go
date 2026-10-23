package task

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

func controlEnvelope(id, kind domain.ID, current domain.Task) domain.Envelope {
	e := localEnvelope(id)
	e.Command.Type = string(kind)
	e.Command.TargetID = current.ID
	e.Command.Payload, _ = json.Marshal(domain.TaskTarget{TaskID: current.ID})
	e.Command.ExpectedVersions = domain.Versions{"task:" + string(current.ID): current.Version}
	e.Scope, e.ClusterID, e.MembershipEpoch = current.Scope, current.ClusterID, current.MembershipEpoch
	e.RequestHash, _ = RequestHash(e.Command)
	return e
}

func TestControlsPersistAndRetryLinkedTask(t *testing.T) {
	ctx := context.Background()
	j, err := OpenJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Shutdown()
	def := engine.TaskDefinition{Kind: "probe", SafeRetry: true, Phases: []engine.Phase{{Name: "read", Interruptible: true, Run: func(ctx context.Context, e engine.Execution) error {
		return e.Observed(ctx, domain.Checkpoint{Phase: "read"})
	}}}}
	s, _ := NewScheduler(j, "node", []engine.TaskDefinition{def})
	r, err := s.Admit(ctx, localEnvelope("original"))
	if err != nil {
		t.Fatal(err)
	}
	original, _ := j.Task(ctx, *r.TaskID)
	cancel := controlEnvelope("cancel", "task.cancel", original)
	accepted, err := s.Control(ctx, cancel)
	if err != nil || *accepted.TaskID != original.ID {
		t.Fatal(accepted, err)
	}
	cancelled, _ := j.Task(ctx, original.ID)
	if cancelled.State != domain.Cancelled || !cancelled.Cancellation.Requested {
		t.Fatal(cancelled)
	}
	// Simulate the crash boundary after receipt persistence but before projection.
	if err = j.write(taskPath(original.ID), original); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Control(ctx, cancel); err != nil {
		t.Fatal(err)
	}
	cancelled, _ = j.Task(ctx, original.ID)
	if cancelled.State != domain.Cancelled {
		t.Fatal(cancelled.State)
	}
	chunk, err := j.Read(ctx, original.ID, nil, 0, 100)
	if err != nil || !chunk.Complete {
		t.Fatal(chunk, err)
	}
	stale := controlEnvelope("stale", "task.retry", original)
	if _, err = s.Control(ctx, stale); err == nil {
		t.Fatal("stale version accepted")
	}
	retry := controlEnvelope("retry", "task.retry", cancelled)
	retried, err := s.Control(ctx, retry)
	if err != nil || *retried.TaskID == original.ID {
		t.Fatal(retried, err)
	}
	next, _ := j.Task(ctx, *retried.TaskID)
	if next.Kind != original.Kind || next.Scope != original.Scope || next.RetryOf == nil || *next.RetryOf != original.ID || next.Version != 1 {
		t.Fatal(next)
	}
	if err = s.Execute(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Control(ctx, retry); err != nil {
		t.Fatal("duplicate after completion", err)
	}
	retry.Command.Payload = json.RawMessage(`{"task_id":"different"}`)
	retry.RequestHash, _ = RequestHash(retry.Command)
	if _, err = s.Control(ctx, retry); err == nil {
		t.Fatal("identity conflict accepted")
	}
	page, err := j.Tasks(ctx, domain.PageRequest{})
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	unchanged, _ := j.Task(ctx, original.ID)
	if unchanged.Version != cancelled.Version || unchanged.State != cancelled.State {
		t.Fatal("terminal task changed")
	}
}

func TestCancelRunningAndRefuseUnsafeRetry(t *testing.T) {
	ctx := context.Background()
	j, err := OpenJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Shutdown()
	started := make(chan struct{})
	def := engine.TaskDefinition{Kind: "probe", SafeRetry: true, Phases: []engine.Phase{{Name: "read", Interruptible: true, Run: func(ctx context.Context, e engine.Execution) error { close(started); <-ctx.Done(); return ctx.Err() }}}}
	s, _ := NewScheduler(j, "node", []engine.TaskDefinition{def})
	r, err := s.Admit(ctx, localEnvelope("running"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Execute(ctx, *r.TaskID) }()
	<-started
	current, _ := j.Task(ctx, *r.TaskID)
	if _, err = s.Control(ctx, controlEnvelope("stop", "task.cancel", current)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt worker")
	}
	current, _ = j.Task(ctx, *r.TaskID)
	if current.State != domain.Cancelled {
		t.Fatal(current)
	}
	// A crash after signalling but before terminal persistence must not rerun
	// cancelled observation work on restart.
	current.State = domain.Cancelling
	if err = j.write(taskPath(current.ID), current); err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ = j.Task(ctx, current.ID)
	if current.State != domain.Cancelled {
		t.Fatal(current)
	}
	def.SafeRetry = false
	s.Definitions[def.Kind] = def
	_, err = s.Control(ctx, controlEnvelope("unsafe", "task.retry", current))
	var detail *domain.Error
	if !errors.As(err, &detail) || detail.Code != "CONFIRMATION_REQUIRED" {
		t.Fatal(err)
	}
	// Foreign scope/epoch cannot control an owner-local task.
	foreign := controlEnvelope("foreign", "task.retry", current)
	foreign.Scope = domain.ClusterScope
	cluster := domain.ID("cluster")
	foreign.ClusterID = &cluster
	if _, err = s.Control(ctx, foreign); err == nil {
		t.Fatal("foreign scope accepted")
	}
}
