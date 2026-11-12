package controllers

import (
	"context"
	"errors"
	"nixmox/examples/jetstream/records"
	"nixmox/examples/jetstream/stores"
)

// Scheduler is invoked by the durable event consumer, NOT the dispatcher.
// Command events must not be coalesced into store dirty flags.
type TaskSchedulerController struct {
	HostID string
	Writes *records.TaskWriter
}

func NewTaskSchedulerController(hostID string, tasks *stores.TaskStore) *TaskSchedulerController {
	return &TaskSchedulerController{HostID: hostID, Writes: &records.TaskWriter{HostID: hostID, KV: tasks.KV}}
}

func (c *TaskSchedulerController) Handle(ctx context.Context, event records.TaskEvent) error {
	// Validate target/type/action and durably deduplicate EventID.
	// Create/change LOCAL task desired state for start/stop/retry.
	// Return only once persistence succeeds, so the consumer can acknowledge.
	// Spawn/register one task controller instance of the matching type (HostID/TaskID).
	// Recover existing task instances at startup without duplicating them.
	// The resulting task KV update reaches its watcher, cache, then dispatcher.
	return errors.ErrUnsupported
}
