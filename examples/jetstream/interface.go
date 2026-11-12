package main

import (
	"context"
	"errors"
)

// These stubs deliberately return ErrUnsupported rather than pretending success.
// main.go retains the API walkthrough; the new architecture is not implemented yet.
func (d *Daemon) CreateCluster(ctx context.Context) error {
	// Initialize host identity and the low-level NATS manager.
	// Call StartPrimary, StartBootstrap and Connect DIRECTLY: no cluster exists yet.
	// Call MembershipStore.Create and TaskStore.Create, then TaskEvents.Create.
	// Create the host's full-stable membership sections through records.MemberWriter.
	// Initialize internal wiring and start independent watches/consumer/dispatcher.
	return errors.ErrUnsupported
}

func (d *Daemon) Enroll(ctx context.Context, seed *Daemon) error {
	// Connect as a client through the seed using the low-level manager.
	// Open existing stores and task-event stream; never provision them here.
	// Create this host's membership sections as client-only FULL, not pending.
	// Initialize internal wiring and start watches/consumer/dispatcher.
	return errors.ErrUnsupported
}

func (d *Daemon) Promote(ctx context.Context, target string) error {
	// Publish a start-promotion command addressed to target: full -> full-stable.
	// Await that target-owned task's state through the task store.
	return errors.ErrUnsupported
}

func (d *Daemon) Demote(ctx context.Context, target string) error {
	// Publish a start-demotion command addressed to target: full-stable -> full.
	// Await target-owned task completion; never edit target membership directly.
	return errors.ErrUnsupported
}

func (d *Daemon) Evict(ctx context.Context, target string) error {
	// Publish a target-owned eviction task; primary stays running/in metadata.
	return errors.ErrUnsupported
}

func (d *Daemon) Remove(ctx context.Context, target string) error {
	// Publish a permanent-removal task, separate from eviction or process cleanup.
	return errors.ErrUnsupported
}

func (d *Daemon) Stop(ctx context.Context, evict bool) error {
	// Request safe local shutdown, optionally starting with host eviction.
	// Each individual server stop also attempts its own eviction.
	return errors.ErrUnsupported
}

func (d *Daemon) Close(ctx context.Context) error {
	// Invoke internal close: stop background activity before manager teardown.
	return errors.ErrUnsupported
}
