// task.go distinguishes delivered task events from durable tracked processes.
package main

import "context"

// TaskEvent is published to tasks.<target>.<action> in the TASK_EVENTS stream.
type TaskEvent struct {
	ID          string `json:"id"`
	RequestedBy string `json:"requestedBy"`
	Target      string `json:"target"`
	Action      string `json:"action"`
	Role        Role   `json:"role"`
}

// TaskState records progress, not instructions to replay without checking reality.
type TaskState struct {
	RoleUpdated        bool   `json:"roleUpdated"`
	TopologyReconciled bool   `json:"topologyReconciled"`
	DataMoved          bool   `json:"dataMoved"`
	PrimaryUpdated     bool   `json:"primaryUpdated"`
	Completed          bool   `json:"completed"`
	Error              string `json:"error,omitempty"`
}

// TaskRecord belongs at TASKS/<target>.<id>, with an immutable request.
type TaskRecord struct {
	Spec  TaskEvent `json:"spec"`
	State TaskState `json:"state"`
}

// TaskController tracks one process owned by its target daemon.
type TaskController struct {
	Daemon *Daemon
	TaskID string
}

var _ Controller = (*TaskController)(nil)

// HandleTaskEvent will create or identify the durable task before acknowledging delivery.
func (d *Daemon) HandleTaskEvent(ctx context.Context, event TaskEvent) error {
	return ErrNotImplemented
}

// Reconcile will advance task progress and local member intent from current observations.
func (c *TaskController) Reconcile(ctx context.Context) error {
	return ErrNotImplemented
}
