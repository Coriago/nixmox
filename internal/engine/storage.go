// Package engine defines shared contracts. Concrete implementations live in the
// comms, telemetry, task, nix and git subpackages; feature workflows live in service.
package engine

import (
	"context"
	"encoding/json"
	"io"

	"nixmox/internal/domain"
)

type Revision uint64 // Storage CAS token, distinct from a resource's lifecycle version.

// LocalStore persists an entire admission record atomically. Implementations must
// fsync data and parent directories before acknowledging; identity reset preserves it.
type LocalStore interface {
	Admit(context.Context, domain.AdmissionRecord, domain.Task) (domain.AdmissionRecord, error)
	Receipt(context.Context, domain.ID) (domain.AdmissionRecord, error)
	Task(context.Context, domain.ID) (domain.Task, error)
	SaveTask(context.Context, domain.Task, domain.Version) error
	Recoverable(context.Context, domain.PageRequest) (domain.Page[domain.Task], error)
}

type StateEntry struct {
	SnapshotComplete bool
	Key              string
	Revision         Revision
	Value            json.RawMessage
	Deleted          bool
}
type StateStore interface {
	Get(context.Context, string) (StateEntry, error)
	CompareAndSwap(context.Context, string, Revision, json.RawMessage) (Revision, error)
	// Watch includes initialization completion; callers reconcile snapshots on reconnect.
	Watch(context.Context, string) (Subscription[StateEntry], error)
}
type ArtifactStore interface {
	// Only incremental Git bundles, never full remote bases or secret values.
	Put(context.Context, domain.Bundle, io.Reader) error
	Open(context.Context, domain.Bundle) (io.ReadCloser, error)
	Delete(context.Context, domain.Bundle) error
}
type LogStore interface {
	Append(context.Context, domain.ID, *domain.ID, []byte) (domain.LogEvent, error)
	Read(context.Context, domain.ID, *domain.ID, int64, int) (domain.LogChunk, error)
	Snapshot(context.Context, domain.ID, *domain.ID) (domain.Download, io.ReadCloser, error)
	Close(context.Context, domain.ID, *domain.ID) error
}

// ProgressStore exposes persisted derived telemetry independently of task state.
type ProgressStore interface {
	Progress(context.Context, domain.ID) (domain.TaskProgress, error)
	Activity(context.Context, domain.ID, domain.ID) (domain.Activity, error)
}

type Subscription[T any] interface {
	Next(context.Context) (T, error)
	Close() error
}
