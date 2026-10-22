package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/storage"
)

func operationStore(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
func testOperation(run func(context.Context, *Execution) (any, error)) Operation {
	return Operation{Kind: "wifi.scan", Scope: "node_local", Timeout: time.Second * 10, Validate: func(c domain.Command) error {
		var p struct {
			Resource string `json:"resource"`
		}
		return domain.StrictJSON(c.Payload, &p)
	}, Run: run}
}
func admitOperation(t *testing.T, s *Service, id, resource string) domain.Receipt {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"resource": resource})
	r, err := s.Admit(domain.Command{ID: id, Type: "wifi.scan", TargetID: s.Identity().NodeID, Payload: b})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResourceExclusionAndIndependentConcurrency(t *testing.T) {
	started := make(chan string, 4)
	release := make(chan struct{})
	op := testOperation(func(ctx context.Context, e *Execution) (any, error) {
		started <- e.Command.ID
		select {
		case <-release:
			return "observed", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	op.Exclusions = func(c domain.Command, resolved json.RawMessage) ([]string, error) {
		var p struct{ Resource string }
		json.Unmarshal(resolved, &p)
		return []string{p.Resource}, nil
	}
	s, err := Open(operationStore(t), inspectFunc(good), op)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := admitOperation(t, s, "a", "disk:a")
	if got := <-started; got != "a" {
		t.Fatal(got)
	}
	b := admitOperation(t, s, "b", "disk:a")
	c := admitOperation(t, s, "c", "disk:b")
	select {
	case got := <-started:
		if got != "c" {
			t.Fatalf("overlapping resource: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("independent work blocked")
	}
	if task, _ := s.Task(b.TaskID); task.State != "queued" {
		t.Fatal(task.State)
	}
	close(release)
	for _, r := range []domain.Receipt{a, b, c} {
		await(t, s, r.TaskID, "succeeded")
	}
}

func TestCheckpointRecoveryUsesReconciliationAndPinnedResolution(t *testing.T) {
	st := operationStore(t)
	var runs, resolutions, reconciliations atomic.Int32
	checkpointed := make(chan struct{})
	op := testOperation(func(ctx context.Context, e *Execution) (any, error) {
		runs.Add(1)
		if err := e.Checkpoint("effect-intent", map[string]string{"output": "pinned"}, false); err != nil {
			return nil, err
		}
		if err := e.Log([]byte("intent persisted\n")); err != nil {
			return nil, err
		}
		close(checkpointed)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	op.Resolve = func(context.Context, domain.Command) (json.RawMessage, error) {
		resolutions.Add(1)
		return json.RawMessage(`{"commit":"immutable"}`), nil
	}
	op.Reconcile = func(ctx context.Context, e *Execution) (any, error) {
		reconciliations.Add(1)
		task, err := e.Snapshot()
		if err != nil {
			return nil, err
		}
		if string(task.Checkpoint) != `{"output":"pinned"}` || string(e.Resolved) != `{"commit":"immutable"}` {
			t.Errorf("lost pinned state: %+v %s", task, e.Resolved)
		}
		return map[string]bool{"effect_observed": true}, nil
	}
	s, err := Open(st, inspectFunc(good), op)
	if err != nil {
		t.Fatal(err)
	}
	r := admitOperation(t, s, "recover", "disk:a")
	<-checkpointed
	task, _ := s.Task(r.TaskID)
	_, err = s.Admit(domain.Command{ID: "cancel", Type: "task.cancel", TargetID: s.Identity().NodeID, ExpectedVersions: map[string]int64{"task:" + task.ID: task.Version}, Payload: json.RawMessage(`{"task_id":"` + task.ID + `"}`)})
	code(t, err, "INVALID_STATE")
	s.Close()
	// A crash can persist bytes before updating the task's log cursor.
	if _, err := st.AppendLog(r.TaskID, []byte("effect receipt\n")); err != nil {
		t.Fatal(err)
	}
	s, err = Open(st, inspectFunc(good), op)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task = await(t, s, r.TaskID, "succeeded")
	if runs.Load() != 1 || resolutions.Load() != 1 || reconciliations.Load() != 1 {
		t.Fatalf("run=%d resolve=%d reconcile=%d", runs.Load(), resolutions.Load(), reconciliations.Load())
	}
	l, err := s.Logs(task.ID, 0, 65536)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := base64.StdEncoding.DecodeString(l.Data)
	if string(b) != "intent persisted\neffect receipt\n" || !l.Complete || task.Log.NextOffset != len(b) {
		t.Fatalf("%+v %q", l, b)
	}
}

func TestBlockedReviewSurvivesRestartAndRequiresVersionedResume(t *testing.T) {
	st := operationStore(t)
	var calls atomic.Int32
	op := testOperation(func(ctx context.Context, e *Execution) (any, error) {
		calls.Add(1)
		return nil, Block("review", "APPROVAL_REQUIRED", "Review candidate.")
	})
	op.Reconcile = func(ctx context.Context, e *Execution) (any, error) { return "verified", nil }
	s, err := Open(st, inspectFunc(good), op)
	if err != nil {
		t.Fatal(err)
	}
	r := admitOperation(t, s, "review", "target:a")
	task := await(t, s, r.TaskID, "blocked")
	s.Close()
	s, err = Open(st, inspectFunc(good), op)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, _ := s.Task(task.ID); got.State != "blocked" || calls.Load() != 1 {
		t.Fatal(got)
	}
	check := func(t domain.Task) error {
		if t.Error.Code != "APPROVAL_REQUIRED" {
			return domain.Err("INVALID_STATE", "Invalid review.", 409)
		}
		return nil
	}
	code(t, s.Resume(task.ID, task.Version-1, check), "VERSION_CONFLICT")
	if err := s.Resume(task.ID, task.Version, check); err != nil {
		t.Fatal(err)
	}
	await(t, s, task.ID, "succeeded")
	code(t, s.Resume(task.ID, task.Version, check), "VERSION_CONFLICT")
}
