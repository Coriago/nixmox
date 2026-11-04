package task_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/task"
	"nixmox/internal/testutil"
)

// Keep storage and watches real; inject the API timeout seen during a peer
// transition at the scheduler's read boundary, without retrying a task runner.
type interruptedKeys struct {
	jetstream.KeyValue
	interrupt atomic.Bool
	failures  atomic.Int32
}

func (k *interruptedKeys) Keys(ctx context.Context, opts ...jetstream.WatchOpt) ([]string, error) {
	if k.interrupt.Swap(false) {
		k.failures.Add(1)
		return nil, context.DeadlineExceeded
	}
	return k.KeyValue.Keys(ctx, opts...)
}

func TestSchedulerSurvivesReadTimeout(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	keys := &interruptedKeys{KeyValue: tasks.KV}
	var executed atomic.Int32
	started := make(chan struct{})
	manager := &task.Manager{HostID: "cedar", Tasks: cluster.Store[domain.Task]{KV: keys}, Resources: task.Resources{Store: locks}, Runners: map[string]task.Runner{
		"hold": func(ctx context.Context, _ *task.Execution) (any, error) {
			executed.Add(1)
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		"example": func(context.Context, *task.Execution) (any, error) { executed.Add(1); return nil, nil },
	}}
	life, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	var runErr error
	go func() { runErr = manager.Run(life); close(done) }()
	defer func() {
		stop()
		<-done
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Error(runErr)
		}
	}()
	submitAndWait := func() {
		t.Helper()
		submitted, err := manager.Submit(ctx, domain.Task{HostID: "cedar", Type: "example", Inputs: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		reached := errors.New("finished")
		err = tasks.Watch(ctx, submitted.ID, func(record *cluster.Record[domain.Task], deleted bool) error {
			if record != nil && !deleted && record.Value.Status == domain.Succeeded {
				return reached
			}
			return nil
		})
		if !errors.Is(err, reached) {
			t.Fatalf("task did not complete after scheduler read: %v", err)
		}
	}

	// --- Usage ---
	// -------------
	if _, err := manager.Submit(ctx, domain.Task{HostID: "cedar", Type: "hold", Inputs: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	keys.interrupt.Store(true)
	submitAndWait()

	// --- Assertions ---
	// ------------------
	select {
	case <-done:
		t.Fatalf("scheduler exited after a transient read: %v", runErr)
	case <-time.After(100 * time.Millisecond):
	}
	if keys.failures.Load() != 1 || executed.Load() != 2 {
		t.Fatal("scheduler must survive the timeout without repeating a runner")
	}
}
