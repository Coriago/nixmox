package service

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"nixmox/internal/domain"
)

func TestMembershipEpochFencesUnfinishedWorkAndNewDeliveries(t *testing.T) {
	st := operationStore(t)
	identity, err := st.Identity()
	if err != nil {
		t.Fatal(err)
	}
	cluster := "c_epoch"
	identity.ClusterID = &cluster
	identity.MembershipEpoch = 1
	identity.Version++
	if err = st.Write("identity.json", identity); err != nil {
		t.Fatal(err)
	}
	var runs, reconciles atomic.Int32
	started := make(chan struct{})
	op := Operation{Kind: "host.refresh", Scope: "cluster", Timeout: time.Minute, SafeRetry: true, Validate: func(c domain.Command) error { var v struct{}; return domain.StrictJSON(c.Payload, &v) }, Run: func(ctx context.Context, e *Execution) (any, error) {
		runs.Add(1)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, Reconcile: func(context.Context, *Execution) (any, error) { reconciles.Add(1); return nil, nil }}
	s, err := Open(st, inspectFunc(good), op)
	if err != nil {
		t.Fatal(err)
	}
	c, fingerprint, err := domain.CanonicalCommand(domain.Command{ID: "cmd_epoch", Type: op.Kind, TargetID: identity.NodeID, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	p := domain.PreparedCommand{Command: c, ClusterID: cluster, CoordinatorID: "n_coordinator", TargetEpoch: 1, Fingerprint: fingerprint, Kind: op.Kind, Resolved: c.Payload, AdmittedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	receipt, err := s.AdmitDelivered(p)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}
	s.Close()
	identity.MembershipEpoch = 2
	identity.Version++
	if err = st.Write("identity.json", identity); err != nil {
		t.Fatal(err)
	}
	s, err = Open(st, inspectFunc(good), op)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Task(receipt.TaskID)
	if err != nil || task.State != "blocked" || task.Phase != "membership_fenced" || task.Cancellation.Allowed {
		t.Fatal("unfinished work escaped its epoch fence", task, err)
	}
	version := task.Version
	s.Close()
	s, err = Open(st, inspectFunc(good), op)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, _ = s.Task(receipt.TaskID)
	if task.Version != version || runs.Load() != 1 || reconciles.Load() != 0 {
		t.Fatal("restart changed fenced review or executed stale work")
	}
	c.ID = "cmd_stale_epoch"
	p.Command, p.Fingerprint, err = domain.CanonicalCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitDelivered(p); err == nil {
		t.Fatal("old-epoch delivery was admitted")
	}
}
