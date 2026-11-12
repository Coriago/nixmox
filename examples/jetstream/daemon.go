package main

import (
	"context"
	"errors"

	"nixmox/examples/jetstream/controllers"
	"nixmox/examples/jetstream/records"
	"nixmox/examples/jetstream/runtime"
	"nixmox/examples/jetstream/stores"
)

// Internal wiring/lifecycle only. Public entry points are in interface.go.
type Daemon struct {
	HostID          string
	NATS            *runtime.NATSManager
	Members         *stores.MembershipStore
	Tasks           *stores.TaskStore
	TaskEvents      *stores.TaskEvents
	Dispatcher      *Dispatcher
	Bootstrap       *controllers.BootstrapController
	NATSService     *controllers.NATSServiceController
	Scheduler       *controllers.TaskSchedulerController
	MembershipTasks map[string]*controllers.MembershipTaskController
	CommandTasks    map[string]*controllers.CommandTaskController
	MemberWrites    *records.MemberWriter
	TaskWrites      *records.TaskWriter
}

func (d *Daemon) initialize(ctx context.Context) error {
	// Construct stores, record writers, controllers and dispatcher.
	// Use each controller's constructor with its concrete stores/records/manager.
	// Give each instance a Wake callback for Dispatcher.ControllerChanged(instance.ID()).
	// Task instances are keyed by task ID, not shared across unrelated tasks.
	// Register controllers using THEIR declared interests, not a routing switch.
	// Scheduler consumes task commands separately; it is not a dispatched controller.
	return errors.ErrUnsupported
}

func (d *Daemon) start(ctx context.Context) error {
	// Start each store's independent Watch handler; each maintains its own cache.
	// Wait for required initial snapshots before enabling dependent controller steps.
	// Start the task-event consumer with Scheduler.Handle as its handler.
	// Request an initial dispatcher pass to evaluate already-satisfied conditions.
	return errors.ErrUnsupported
}

func (d *Daemon) close(ctx context.Context) error {
	// Stop accepting commands; cancel watches and dispatcher scheduling.
	// Call Stop on each controller to cancel/join its jobs, then close the NATS manager.
	// Stop must not run concurrently with Step; ignore wake-ups once dispatch is stopped.
	// Example temporary-storage cleanup is distinct from membership retirement.
	return errors.ErrUnsupported
}
