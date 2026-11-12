package controllers

import (
	"context"
	"errors"
	"nixmox/examples/jetstream/records"
	"nixmox/examples/jetstream/stores"
)

type CommandTaskController struct {
	Base
	TaskID string // One controller instance per task.
	Writes *records.TaskWriter
}

func NewCommandTaskController(hostID, taskID string, tasks *stores.TaskStore, wake func()) *CommandTaskController {
	return &CommandTaskController{Base: NewBase(hostID, wake), TaskID: taskID, Writes: &records.TaskWriter{HostID: hostID, KV: tasks.KV}}
}

func (c *CommandTaskController) ID() string             { return "command-task/" + c.TaskID }
func (c *CommandTaskController) Interests() []stores.ID { return []stores.ID{stores.Tasks} }

func (c *CommandTaskController) Step(ctx context.Context, input stores.Snapshots) {
	// Inspect only HostID/TaskID. Membership changes never trigger this instance.
	// Start/cancel commands asynchronously, tracking each task's in-flight execution.
	// Initiate status writes separately; completion wakes this controller directly.
}

func (c *CommandTaskController) executeCommand(ctx context.Context, task records.TaskRecord) error {
	// Run command with cancellation; safely hand progress/result back and Wake.
	return errors.ErrUnsupported
}

func (c *CommandTaskController) executeProgress(ctx context.Context, taskID string, state records.TaskState) error {
	// Update only this local command task's state.
	return errors.ErrUnsupported
}
