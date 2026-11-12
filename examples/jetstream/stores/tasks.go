package stores

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/examples/jetstream/records"
)

type TaskSnapshot struct {
	Ready bool
	Tasks map[string]records.TaskRecord // host.task -> assembled desired/state
}

type TaskStore struct {
	JS    jetstream.JetStream
	KV    jetstream.KeyValue
	cache TaskSnapshot
}

func (s *TaskStore) Create(ctx context.Context) error {
	// Create TASKS with initial replication; do not overwrite existing configuration.
	return errors.ErrUnsupported
}

func (s *TaskStore) Open(ctx context.Context) error {
	// Open existing TASKS only.
	return errors.ErrUnsupported
}

func (s *TaskStore) UpdateReplicas(ctx context.Context, count int) error {
	// Update ONLY this bucket's backing stream; wait for required replicas.
	return errors.ErrUnsupported
}

func (s *TaskStore) Watch(ctx context.Context, observer Observer) error {
	// Independent KV WatchAll and handler, not a select loop shared with other buckets.
	// Decode desired/state sections; set their Revision from entry.Revision().
	// Update cache, then call StoreChanged(Tasks).
	// Track initialization, deletions/revisions and partial task creation.
	return errors.ErrUnsupported
}

func (s *TaskStore) Snapshot() TaskSnapshot {
	// Return an independent read view of current cache; no fetch per controller.
	return TaskSnapshot{}
}

func (s *TaskStore) Wait(ctx context.Context, hostID, taskID string) error {
	// Observe this store's existing watch/cache notifications until terminal task state.
	// Evaluate immediately too: task may have completed before Wait was called.
	return errors.ErrUnsupported
}
