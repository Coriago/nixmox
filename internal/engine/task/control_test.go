package task

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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

func TestCancelAndExplicitSafeRetry(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{}, 2)
	def := engine.TaskDefinition{SafeRetry: true, Phase: "read", Interruptible: true, Run: func(ctx context.Context, e engine.Execution) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}}
	m := NewManager(ctx, "node")
	defer m.Close()
	r, err := m.Accept(ctx, localEnvelope("original"), def, acknowledged)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	current, _ := m.Task(ctx, *r.TaskID)
	cancel := controlEnvelope("cancel", "task.cancel", current)
	if _, err := m.Accept(ctx, cancel, def, acknowledged); err != nil {
		t.Fatal(err)
	}
	done := awaitTask(t, m, current.ID)
	if done.State != domain.Cancelled || !done.Cancellation.Requested {
		t.Fatal(done)
	}
	if _, err := m.Accept(ctx, cancel, def, acknowledged); err != nil {
		t.Fatal("duplicate cancel", err)
	}
	if _, err := m.Accept(ctx, controlEnvelope("stale", "task.retry", current), def, acknowledged); err == nil {
		t.Fatal("stale control accepted")
	}
	retry := controlEnvelope("retry", "task.retry", done)
	r, err = m.Accept(ctx, retry, def, acknowledged)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	next, _ := m.Task(ctx, *r.TaskID)
	if next.RetryOf == nil || *next.RetryOf != current.ID || next.Scope != current.Scope {
		t.Fatal(next)
	}
	foreign := controlEnvelope("foreign", "task.cancel", next)
	foreign.Scope = domain.ClusterScope
	if _, err := m.Accept(ctx, foreign, def, acknowledged); err == nil {
		t.Fatal("foreign scope accepted")
	}
	if _, err := m.Accept(ctx, controlEnvelope("stop", "task.cancel", next), def, acknowledged); err != nil {
		t.Fatal(err)
	}
	done = awaitTask(t, m, next.ID)
	def.SafeRetry = false
	def.Run = func(context.Context, engine.Execution) error { return nil }
	unsafe, err := m.Accept(ctx, localEnvelope("unsafe-original"), def, acknowledged)
	if err != nil {
		t.Fatal(err)
	}
	done = awaitTask(t, m, *unsafe.TaskID)
	_, err = m.Accept(ctx, controlEnvelope("unsafe", "task.retry", done), def, acknowledged)
	var detail *domain.Error
	if !errors.As(err, &detail) || detail.Code != "CONFIRMATION_REQUIRED" {
		t.Fatal(err)
	}
}
