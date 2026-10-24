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

func acknowledged(context.Context) error { return nil }

func awaitTask(t *testing.T, m *Manager, id domain.ID) domain.Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := m.Task(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if terminal(current.State) {
			return current
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task did not finish")
	return domain.Task{}
}

func TestAcknowledgementBeforeExecutionAndSessionOnlyHistory(t *testing.T) {
	ctx := context.Background()
	var effects atomic.Int32
	var acked atomic.Bool
	def := engine.TaskDefinition{Phase: "effect", Run: func(ctx context.Context, e engine.Execution) error {
		if !acked.Load() {
			t.Error("effect before confirmed ack")
		}
		effects.Add(1)
		_, _ = e.Log().Write([]byte("output"))
		return e.Result(ctx, map[string]any{})
	}}
	m := NewManager(ctx, "node")
	envelope := localEnvelope("command")
	if _, err := m.Accept(ctx, envelope, def, func(context.Context) error { return errors.New("lost ack") }); err == nil {
		t.Fatal("unconfirmed ack accepted")
	}
	if len(m.List()) != 0 || effects.Load() != 0 {
		t.Fatal("work started despite ack failure")
	}
	r, err := m.Accept(ctx, envelope, def, func(context.Context) error { acked.Store(true); return nil })
	if err != nil {
		t.Fatal(err)
	}
	current := awaitTask(t, m, *r.TaskID)
	if current.State != domain.Succeeded {
		t.Fatal(current)
	}
	envelope.Command.Payload = []byte(`{"b":2,"a":1}`)
	if _, err := m.Accept(ctx, envelope, def, acknowledged); err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 1 {
		t.Fatal("session duplicate executed")
	}
	envelope.Command.Payload = []byte(`{"a":3}`)
	if _, err := m.Accept(ctx, envelope, def, acknowledged); err == nil {
		t.Fatal("conflicting ID accepted")
	}
	chunk, err := m.Read(ctx, current.ID, nil, 0, 100)
	if err != nil || !chunk.Complete || chunk.NextOffset != 6 {
		t.Fatal(chunk, err)
	}
	if _, err := m.Append(ctx, current.ID, nil, []byte("late")); err == nil {
		t.Fatal("terminal log appended")
	}
	m.Close()
	next := NewManager(ctx, "node")
	defer next.Close()
	if len(next.List()) != 0 {
		t.Fatal("history survived new manager")
	}
	if _, err := next.Receipt(ctx, envelope.Command.ID); err == nil {
		t.Fatal("receipt survived restart")
	}
	if effects.Load() != 1 {
		t.Fatal("restart replayed work")
	}
}

func TestBusyWorkStaysUnacknowledged(t *testing.T) {
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	def := engine.TaskDefinition{Phase: "hold", Resources: []string{"host"}, Run: func(context.Context, engine.Execution) error {
		close(started)
		<-release
		return nil
	}}
	m := NewManager(ctx, "node")
	defer m.Close()
	r, err := m.Accept(ctx, localEnvelope("first"), def, acknowledged)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	acked := false
	_, err = m.Accept(ctx, localEnvelope("second"), def, func(context.Context) error { acked = true; return nil })
	close(release)
	if !errors.Is(err, ErrResourceBusy) || acked {
		t.Fatal("busy work consumed", err)
	}
	awaitTask(t, m, *r.TaskID)
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
