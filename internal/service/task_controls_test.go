package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"nixmox/internal/domain"
	"nixmox/internal/engine/task"
)

func TestLocalTaskControlsInheritScope(t *testing.T) {
	ctx := context.Background()
	j, err := task.OpenJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Shutdown()
	s := New()
	s.Identity.NodeID = "node"
	s.Journal = j
	s.Network = func(context.Context) (domain.NetworkStatus, error) { return domain.NetworkStatus{}, nil }
	if err = s.StartNode(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := s.Submit(ctx, domain.Command{ID: "observe", Type: "network.check", TargetID: "node", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	original, _ := j.Task(ctx, *r.TaskID)
	command := func(id, kind string, version domain.Version) domain.Command {
		data, _ := json.Marshal(domain.TaskTarget{TaskID: original.ID})
		return domain.Command{ID: domain.ID(id), Type: kind, TargetID: original.ID, ExpectedVersions: domain.Versions{"task:" + string(original.ID): version}, Payload: data}
	}
	// Local controls must never consult cluster transport, even after joining.
	s.Cluster = &ClusterNode{}
	cancel := command("cancel", "task.cancel", original.Version)
	receipt, err := s.Submit(ctx, cancel)
	if err != nil || *receipt.TaskID != original.ID {
		t.Fatal(receipt, err)
	}
	cancelled, _ := j.Task(ctx, original.ID)
	if cancelled.State != domain.Cancelled {
		t.Fatal(cancelled)
	}
	retry := command("retry", "task.retry", cancelled.Version)
	retried, err := s.Submit(ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := j.Task(ctx, *retried.TaskID)
	if next.Scope != domain.NodeLocal || next.ClusterID != nil || next.OwnerNodeID != "node" || next.RetryOf == nil || *next.RetryOf != original.ID {
		t.Fatal(next)
	}
	if err = s.Scheduler.Execute(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []domain.Command{cancel, retry} {
		if _, err = s.Submit(ctx, c); err != nil {
			t.Fatal("duplicate control", err)
		}
	}
	stale := command("stale", "task.retry", original.Version)
	_, err = s.Submit(ctx, stale)
	var detail *domain.Error
	if !errors.As(err, &detail) || detail.Code != "VERSION_CONFLICT" {
		t.Fatal(err)
	}
	invalid := command("wrong-target", "task.cancel", cancelled.Version)
	invalid.TargetID = "node"
	if _, err = s.Submit(ctx, invalid); err == nil {
		t.Fatal("mismatched target accepted")
	}
}
