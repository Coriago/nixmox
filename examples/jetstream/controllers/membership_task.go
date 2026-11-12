package controllers

import (
	"context"
	"errors"
	"nixmox/examples/jetstream/records"
	"nixmox/examples/jetstream/stores"
)

type MembershipTaskController struct {
	Base
	TaskID  string // One controller instance per task.
	Members *records.MemberWriter
	Tasks   *records.TaskWriter
}

func NewMembershipTaskController(hostID, taskID string, members *stores.MembershipStore, tasks *stores.TaskStore, wake func()) *MembershipTaskController {
	return &MembershipTaskController{
		Base: NewBase(hostID, wake), TaskID: taskID,
		Members: &records.MemberWriter{HostID: hostID, KV: members.KV},
		Tasks:   &records.TaskWriter{HostID: hostID, KV: tasks.KV},
	}
}

func (c *MembershipTaskController) ID() string { return "membership-task/" + c.TaskID }
func (c *MembershipTaskController) Interests() []stores.ID {
	return []stores.ID{stores.Tasks, stores.Membership}
}

func (c *MembershipTaskController) Step(ctx context.Context, input stores.Snapshots) {
	// Inspect only HostID/TaskID: this instance owns that one membership task.
	// One Step evaluates current state, not a backlog of membership events.
	// Respect local membership transition exclusivity; track this task's executions.
	// Promotion: request primary, wait for readiness/replication/bootstrap policy,
	// then finalize full-stable role and complete the task.
	// Demotion: guard last full-stable; publish desired role + primary eviction intent.
	// Join primary eviction and outgoing bootstrap eviction/shutdown plus replacement
	// readiness; only then request primary retirement. Finalize role as full.
	// Eviction alone leaves primary running and in metadata membership.
	// Match results to the current task; attempted is not completed evacuation.
	// Initiate the required typed writes asynchronously; return promptly.
}

func (c *MembershipTaskController) executeIntent(ctx context.Context, value records.MembershipDesired) error {
	// Write local desired.membership; never modify another host's records.
	return errors.ErrUnsupported
}

func (c *MembershipTaskController) executePrimaryIntent(ctx context.Context, value records.PrimaryDesired) error {
	// Write local desired.primary (enabled/evict) independently of membership intent.
	return errors.ErrUnsupported
}

func (c *MembershipTaskController) executeRole(ctx context.Context, value records.MembershipState) error {
	// Publish finalized actual membership role after required observed conditions.
	return errors.ErrUnsupported
}

func (c *MembershipTaskController) executeProgress(ctx context.Context, taskID string, state records.TaskState) error {
	// Publish this local membership task's state. Completion requests another Step.
	return errors.ErrUnsupported
}
