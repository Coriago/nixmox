package main

import (
	"context"

	"nixmox/examples/jetstream/controllers"
	"nixmox/examples/jetstream/stores"
)

// One dispatcher per host. No event backlog, WorkKey queues or worker pools.
// The synchronization strategy for these flags is intentionally not implemented.
type Dispatcher struct {
	Members          *stores.MembershipStore
	Tasks            *stores.TaskStore
	registered       map[string]controllers.Controller
	running          bool
	dirtyStores      map[stores.ID]bool
	dirtyControllers map[string]bool // Execution completion can wake a controller directly.
}

func (d *Dispatcher) Register(controller controllers.Controller) {
	// Register its ID and declared store interests.
	// The dispatcher must not contain controller-specific routing rules.
}

func (d *Dispatcher) StoreChanged(store stores.ID) {
	// Called AFTER that store updates its cache.
	// Atomically mark the store dirty. If idle, claim running and initiate run.
	// If already running, return: any number of updates coalesce into the flag.
}

func (d *Dispatcher) ControllerChanged(id string) {
	// Mark this controller dirty after execution completion/failure/local runtime change.
	// Start a pass only if idle, using the same synchronized running/dirty handoff.
}

func (d *Dispatcher) run(ctx context.Context) {
	// Atomically take and CLEAR dirty flags BEFORE taking current store snapshots.
	// Capture current immutable snapshots; never retain a mutable cache reference.
	// For each registered controller whose declared interests intersect dirty stores,
	// or whose direct dirty flag is set: call Step(ctx, snapshots).
	// Step is quick: inspect state and initiate/cancel execution, not await IO.
	// Steps are serialized by this single loop; executions can overlap where safe.
	// After a pass, atomically check dirty flags:
	//   dirty -> another pass with fresh snapshots;
	//   clean -> set running=false and exit in the SAME synchronized handoff.
	// Do not run controllers whose required stores haven't completed initial sync.
}
