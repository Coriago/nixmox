package task_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/task"
	"nixmox/internal/testutil"
)

// TestReviewAndRetry documents build-once, review, approve, and retry behavior
// against real persistent JetStream over mutually authenticated TLS.
func TestReviewAndRetry(t *testing.T) {
	// --- Test setup ---
	// ------------------
	js := testutil.JetStream(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, bucket := range []string{"TASKS", "OUTPUTS", "LOCKS"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, Storage: jetstream.FileStorage}); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := cluster.OpenStore[domain.Task](ctx, js, "TASKS")
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := cluster.OpenStore[domain.TaskOutput](ctx, js, "OUTPUTS")
	if err != nil {
		t.Fatal(err)
	}
	locks, err := cluster.OpenStore[map[string]string](ctx, js, "LOCKS")
	if err != nil {
		t.Fatal(err)
	}
	m := &task.Manager{HostID: "cedar", Tasks: tasks, Outputs: outputs, Resources: task.Resources{Store: locks}, Runners: map[string]task.Runner{}}
	m.Runners["deployment"] = func(ctx context.Context, e *task.Execution) (any, error) {
		if _, ok := e.Task().Context["built"]; !ok {
			if err := e.Checkpoint(ctx, "built", "/nix/store/example"); err != nil {
				return nil, err
			}
		}
		if err := e.Review(ctx, "deployment-diff", map[string]string{"proposed": "/nix/store/example"}); err != nil {
			return nil, err
		}
		return map[string]bool{"activated": true}, nil
	}
	life, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- m.Run(life) }()
	defer func() {
		stop()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	wait := func(id string, status domain.TaskStatus) domain.Task {
		t.Helper()
		var found domain.Task
		reached := errors.New("reached requested state")
		err := tasks.Watch(ctx, id, func(record *cluster.Record[domain.Task], deleted bool) error {
			if record != nil && !deleted && record.Value.Status == status {
				found = record.Value
				return reached
			}
			return nil
		})
		if !errors.Is(err, reached) {
			t.Fatalf("waiting for %s: %v", status, err)
		}
		return found
	}

	// --- Usage ---
	// -------------
	submitted, err := m.Submit(ctx, domain.Task{HostID: "cedar", Type: "deployment", Inputs: json.RawMessage(`{}`), Concurrency: domain.TaskConcurrency{ExclusiveResources: []string{"host:cedar:system"}}})
	if err != nil {
		t.Fatal(err)
	}
	review := wait(submitted.ID, domain.ManualReview)
	if err = m.Approve(ctx, submitted.ID, "stale-output"); err == nil {
		t.Fatal("stale review approved")
	}
	if err = m.Approve(ctx, submitted.ID, review.Review.OutputID); err != nil {
		t.Fatal(err)
	}
	completed := wait(submitted.ID, domain.Succeeded)
	retry, err := m.Retry(ctx, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	wait(retry.ID, domain.ManualReview)
	if err = m.Cancel(ctx, retry.ID); err != nil {
		t.Fatal(err)
	}
	wait(retry.ID, domain.Cancelled)

	// --- Assertions ---
	// ------------------
	if retry.ID == completed.ID || retry.Links.RetryOfTaskID != completed.ID {
		t.Fatal("retry did not preserve provenance")
	}
	if len(completed.Links.Outputs) != 1 || string(completed.Context["built"]) != `"/nix/store/example"` {
		t.Fatal("checkpoint or review output lost")
	}
	if string(completed.Result) != `{"activated":true}` {
		t.Fatalf("unexpected result: %s", completed.Result)
	}
}
