package records

import (
	"context"
	"errors"
	"github.com/nats-io/nats.go/jetstream"
)

type TaskKind string

const (
	Promotion TaskKind = "promotion"
	Demotion  TaskKind = "demotion"
	Removal   TaskKind = "removal"
	Eviction  TaskKind = "eviction"
	Command   TaskKind = "command"
)

// TaskEvent is a command, NOT a KV watch notification.
// TASK_EVENTS subjects: taskevents.<targetHost>.<start|stop|retry>.
type TaskEvent struct {
	EventID    string
	HostID     string
	TaskID     string
	Action     string
	Kind       TaskKind
	TargetRole Role
	Command    []string
}

// Read view assembled from TASKS/<hostID>.<taskID>.desired and .state.
type TaskRecord struct {
	HostID  string
	ID      string
	Desired TaskDesired // taskSchedulerController only
	State   TaskState   // membershipTaskController OR commandTaskController, by Kind
}

type TaskDesired struct {
	Revision    uint64 `json:"-"` // KV entry revision; never persisted in the value.
	Kind        TaskKind
	Action      string
	LastEventID string
	TargetRole  Role
	Command     []string
}

type TaskState struct {
	Revision     uint64 `json:"-"` // KV entry revision; never persisted in the value.
	Phase        string
	OperationID  string
	Dependencies []Dependency
	Error        string
}

type Dependency struct {
	Kind   string // topology, bootstrap-readiness, local-primary, local-bootstrap
	HostID string // Optional: topology dependencies are not restricted to one host.
}

type TaskWriter struct {
	HostID string
	KV     jetstream.KeyValue
}

func (w *TaskWriter) UpdateTaskDesired(ctx context.Context, taskID string, value TaskDesired) error {
	// Derive <localHost>.<taskID>.desired; validate IDs.
	// Use value.Revision: zero means create-only; otherwise conditional update.
	// Scheduler owns this section.
	return errors.ErrUnsupported
}

func (w *TaskWriter) UpdateTaskState(ctx context.Context, taskID string, value TaskState) error {
	// Derive <localHost>.<taskID>.state and conditionally replace using value.Revision.
	// Membership OR command controller owns it, according to task type.
	return errors.ErrUnsupported
}
