package task

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

func localEnvelope(id domain.ID) domain.Envelope {
	return domain.Envelope{SchemaVersion: 1, Scope: domain.NodeLocal, OwnerNodeID: "node", Command: domain.Command{ID: id, Type: "probe", TargetID: "node", Payload: []byte(`{"a":1,"b":2}`)}, ExpiresAt: time.Now().Add(time.Minute)}
}

func TestTaskAdmissionRecoveryAndFencing(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if other, e := OpenJournal(dir); e == nil {
		other.Shutdown()
		t.Fatal("second execution owner accepted")
	}
	var effects, reconciles atomic.Int32
	definition := engine.TaskDefinition{Kind: "probe", Phases: []engine.Phase{{Name: "effect", Interruptible: false, Resources: []engine.Resource{{Key: "target:node", Capacity: 1}}, Run: func(ctx context.Context, e engine.Execution) error {
		effects.Add(1)
		_, _ = e.Log().Write([]byte("before disconnect\n"))
		return errors.New("connection lost")
	}, Reconcile: func(ctx context.Context, e engine.Execution) error {
		reconciles.Add(1)
		return e.Observed(ctx, domain.Checkpoint{Phase: "effect", Evidence: []byte(`{"verified":true}`)})
	}}}}
	scheduler, err := NewScheduler(store, "node", []engine.TaskDefinition{definition})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	envelope := localEnvelope("cmd")
	receipt, err := scheduler.Admit(ctx, envelope)
	if err != nil {
		t.Fatal(err)
	}
	envelope.Command.Payload = []byte(`{ "b": 2, "a": 1 }`)
	duplicate, err := scheduler.Admit(ctx, envelope)
	if err != nil || *duplicate.TaskID != *receipt.TaskID {
		t.Fatalf("retry changed identity: %v", err)
	}
	envelope.Command.Payload = []byte(`{"a":3}`)
	if _, err = scheduler.Admit(ctx, envelope); err == nil {
		t.Fatal("fingerprint conflict accepted")
	}
	if err = scheduler.Execute(ctx, *receipt.TaskID); err == nil {
		t.Fatal("uncertain effect succeeded")
	}
	task, err := store.Task(ctx, *receipt.TaskID)
	if err != nil || task.State != domain.Reconciling {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	second, err := scheduler.Admit(ctx, localEnvelope("second"))
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Execute(ctx, *second.TaskID); !errors.Is(err, ErrResourceBusy) {
		t.Fatalf("uncertain lock released: %v", err)
	}
	store.Shutdown()
	store, err = OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	scheduler, err = NewScheduler(store, "node", []engine.TaskDefinition{definition})
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Execute(ctx, *second.TaskID); !errors.Is(err, ErrResourceBusy) {
		t.Fatal("restart lost exclusion", err)
	}
	if err = scheduler.Execute(ctx, *receipt.TaskID); err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 1 || reconciles.Load() != 1 {
		t.Fatalf("effects replayed: %d/%d", effects.Load(), reconciles.Load())
	}
	task, _ = store.Task(ctx, *receipt.TaskID)
	if task.State != domain.Succeeded {
		t.Fatal(task.State)
	}
	task.Version++
	if err = store.SaveTask(ctx, task, task.Version-1); err == nil {
		t.Fatal("terminal task mutable")
	}
	chunk, err := store.Read(ctx, task.ID, nil, 0, 4096)
	if err != nil || !chunk.Complete || chunk.NextOffset != 18 {
		t.Fatalf("log=%+v %v", chunk, err)
	}
	if _, err = store.Read(ctx, task.ID, nil, 100, 4096); err == nil {
		t.Fatal("out of bounds accepted")
	}
	if _, err = store.Append(ctx, task.ID, nil, []byte("late")); err == nil {
		t.Fatal("closed log appended")
	}
	_, r, err := store.Snapshot(ctx, task.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil || string(data) != "before disconnect\n" {
		t.Fatal(string(data), err)
	}
}

func TestAdmissionProjectionRecovery(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenJournal(dir)
	defer store.Shutdown()
	scheduler, _ := NewScheduler(store, "node", []engine.TaskDefinition{{Kind: "probe", Phases: []engine.Phase{{Name: "read", Run: func(context.Context, engine.Execution) error { return nil }}}}})
	ctx := context.Background()
	e := localEnvelope("interrupted")
	receipt, err := scheduler.Admit(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.root.Remove(taskPath(*receipt.TaskID)); err != nil {
		t.Fatal(err)
	}
	e.ExpiresAt = time.Now().Add(-time.Hour)
	got, err := scheduler.Admit(ctx, e)
	if err != nil || *got.TaskID != *receipt.TaskID {
		t.Fatal("admitted receipt lost after expiry", err)
	}
	if _, err = store.Task(ctx, *got.TaskID); err != nil {
		t.Fatal(err)
	}
}

func TestProcessCancellationAndSensitiveOutput(t *testing.T) {
	t.Setenv("NIXMOX_TEST_SECRET", "must-not-inherit")
	runner := ProcessRunner{Grace: 50 * time.Millisecond}
	var output bytes.Buffer
	result, err := runner.Run(context.Background(), engine.CommandLine{Program: "sh", Args: []string{"-c", `printf '%s' "$NIXMOX_TEST_SECRET"`}}, &output, &output)
	if err != nil || result.ExitCode != 0 || output.Len() != 0 {
		t.Fatal("ambient secret inherited", err, output.String())
	}
	_, err = runner.Run(context.Background(), engine.CommandLine{Program: "sh", Args: []string{"-c", "printf secret"}, SensitiveOutput: true}, &output, &output)
	if err != nil || output.Len() != 0 {
		t.Fatal("sensitive output logged", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = runner.Run(ctx, engine.CommandLine{Program: "sh", Args: []string{"-c", "trap '' TERM; sleep 30 & wait"}}, io.Discard, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("process group did not stop: %v", err)
	}
}
