package task_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/task"
	"nixmox/internal/testutil"
)

// TestPersistentTaskRecovery demonstrates a transition checkpoint, daemon stop,
// and validation on the next daemon lifetime without releasing the system lock.
func TestPersistentTaskRecovery(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	for _, bucket := range []string{"TASKS", "LOCKS"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := cluster.OpenStore[domain.Task](ctx, js, "TASKS")
	if err != nil {
		t.Fatal(err)
	}
	locks, err := cluster.OpenStore[map[string]string](ctx, js, "LOCKS")
	if err != nil {
		t.Fatal(err)
	}
	journal := &task.Journal{Directory: filepath.Join(t.TempDir(), "tasks"), HostID: "cedar"}
	checkpointed := make(chan struct{})
	runner := func(ctx context.Context, execution *task.Execution) (any, error) {
		if _, exists := execution.Task().Context["prepared"]; !exists {
			if err := execution.Checkpoint(ctx, "prepared", "expected-system"); err != nil {
				return nil, err
			}
			close(checkpointed)
			return nil, task.ErrRestart
		}
		return map[string]bool{"validated": true}, nil
	}
	manager := func() *task.Manager {
		return &task.Manager{HostID: "cedar", Tasks: tasks, Resources: task.Resources{Store: locks}, Journal: journal, Runners: map[string]task.Runner{"transition": runner}}
	}
	first := manager()
	life, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- first.Run(life) }()

	// --- Usage ---
	// -------------
	submitted, err := first.Submit(ctx, domain.Task{HostID: "cedar", Type: "transition", Persist: true, Inputs: json.RawMessage(`{}`), Concurrency: domain.TaskConcurrency{ExclusiveResources: []string{"host:cedar:system"}}})
	if err != nil {
		stop()
		<-done
		t.Fatal(err)
	}
	select {
	case <-checkpointed:
	case <-ctx.Done():
		stop()
		<-done
		t.Fatal(ctx.Err())
	}
	stop()
	if err = <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	interrupted, err := tasks.Get(ctx, submitted.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(journal.Directory, submitted.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot domain.Task
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	owners, err := locks.Get(ctx, "host.cedar")
	if err != nil {
		t.Fatal(err)
	}
	second := manager()
	life, stop = context.WithCancel(ctx)
	go func() { done <- second.Run(life) }()
	secondStopped := false
	defer func() {
		if !secondStopped {
			stop()
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		}
	}()
	completed := errors.New("completed")
	var result domain.Task
	err = tasks.Watch(ctx, submitted.ID, func(record *cluster.Record[domain.Task], deleted bool) error {
		if record != nil && !deleted && record.Value.Status.Terminal() {
			result = record.Value
			return completed
		}
		return nil
	})
	if !errors.Is(err, completed) {
		t.Fatal(err)
	}
	// Stop waits for terminal cleanup, not just the shared status notification.
	stop()
	err = <-done
	secondStopped = true
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if interrupted.Value.Status != domain.Running || snapshot.Status != domain.Running || string(snapshot.Context["prepared"]) != `"expected-system"` {
		t.Fatal("persistent checkpoint was not retained")
	}
	if owners.Value["host:cedar:system"] != submitted.ID {
		t.Fatal("restart released the system lock")
	}
	if result.Status != domain.Succeeded || string(result.Result) != `{"validated":true}` || !result.StartedAt.Equal(*interrupted.Value.StartedAt) {
		t.Fatalf("recovery did not validate original task: %+v", result)
	}
	if _, err = os.Stat(filepath.Join(journal.Directory, submitted.ID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("terminal snapshot was not removed")
	}
}
