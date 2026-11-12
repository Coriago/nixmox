package controllers

import (
	"context"
	"errors"
	"nixmox/examples/jetstream/records"
	"nixmox/examples/jetstream/runtime"
	"nixmox/examples/jetstream/stores"
)

type NATSServiceController struct {
	Base
	NATS    *runtime.NATSManager
	Members *stores.MembershipStore
	Writes  *records.MemberWriter
}

func NewNATSServiceController(hostID string, manager *runtime.NATSManager, members *stores.MembershipStore, wake func()) *NATSServiceController {
	return &NATSServiceController{
		Base: NewBase(hostID, wake), NATS: manager, Members: members,
		Writes: &records.MemberWriter{HostID: hostID, KV: members.KV},
	}
}

func (c *NATSServiceController) ID() string             { return "nats-service" }
func (c *NATSServiceController) Interests() []stores.ID { return []stores.ID{stores.Membership} }

func (c *NATSServiceController) Step(ctx context.Context, input stores.Snapshots) {
	// Inspect local desired/state, relevant topology and registered jobs.
	// Handle completed results; cancel jobs that are no longer wanted.
	// Start the needed named jobs, for example:
	//   c.StartJob(ctx, "start-primary", c.startPrimary)
	// Do not duplicate jobs or overlap conflicting manager calls.
	// Return immediately; the jobs perform IO and report progress/completion.
}

func (c *NATSServiceController) startPrimary(ctx context.Context) error {
	// Call NATS.StartPrimary with the appropriate routes and observe readiness.
	// Publish local primary status through the record writer.
	return errors.ErrUnsupported
}

func (c *NATSServiceController) startBootstrap(ctx context.Context) error {
	// Call NATS.StartBootstrap and observe readiness.
	// Publish local bootstrap status through the record writer.
	return errors.ErrUnsupported
}

func (c *NATSServiceController) evictPrimary(ctx context.Context) error {
	// Perform replica eviction separately from the NATS manager.
	// Report attempted/completed and remaining replicas.
	// Do not stop primary or remove metadata membership.
	return errors.ErrUnsupported
}

func (c *NATSServiceController) stopPrimary(ctx context.Context) error {
	// Arrange eviction and any required metadata removal outside the NATS manager.
	// Once shutdown prerequisites are satisfied, call NATS.StopPrimary and publish status.
	return errors.ErrUnsupported
}

func (c *NATSServiceController) stopBootstrap(ctx context.Context) error {
	// Arrange handover, eviction and any metadata removal outside the NATS manager.
	// Then call NATS.StopBootstrap; publish eviction and shutdown results separately.
	return errors.ErrUnsupported
}
