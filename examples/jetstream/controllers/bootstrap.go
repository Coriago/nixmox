package controllers

import (
	"context"
	"errors"
	"nixmox/examples/jetstream/records"
	"nixmox/examples/jetstream/stores"
)

type BootstrapController struct {
	Base
	Writes *records.MemberWriter
}

// Construct after the membership store has been created/opened.
func NewBootstrapController(hostID string, members *stores.MembershipStore, wake func()) *BootstrapController {
	return &BootstrapController{Base: NewBase(hostID, wake), Writes: &records.MemberWriter{HostID: hostID, KV: members.KV}}
}

func (c *BootstrapController) ID() string             { return "bootstrap" }
func (c *BootstrapController) Interests() []stores.ID { return []stores.ID{stores.Membership} }

func (c *BootstrapController) Step(ctx context.Context, input stores.Snapshots) {
	// Inspect current topology; ignore unsynchronized/partial membership.
	// Derive placement deterministically and preserve an eligible existing owner.
	// Exclude hosts with desired.primary.evict or a demoting membership role from placement.
	// Only selected host requests its own bootstrap.
	// Outgoing owner retains intent until the required replacement is ready.
	// Scale-up retirement waits for surviving topology/replication readiness.
	// If local intent needs changing, initiate executeIntent asynchronously.
	// Track in-flight writes; do not launch duplicates. Return without waiting.
}

func (c *BootstrapController) executeIntent(ctx context.Context, value records.BootstrapDesired) error {
	// Write only local desired.bootstrap through the typed writer.
	// Hand result back safely and Wake; conflicts require a fresh Step snapshot.
	return errors.ErrUnsupported
}
