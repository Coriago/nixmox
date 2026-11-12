package stores

import "context"

type ID string

const (
	Membership ID = "membership"
	Tasks      ID = "tasks"
)

// Each bucket implements its own lifecycle and owns its own cache.
// Open never creates/reconfigures; replication changes are explicit.
type Store interface {
	Create(context.Context) error
	Open(context.Context) error
	UpdateReplicas(context.Context, int) error
	Watch(context.Context, Observer) error
}

type Observer interface{ StoreChanged(ID) }

// Supplied at dispatch time, never queued with a notification.
// Snapshots are independent read views, not an atomic cross-bucket transaction.
type Snapshots struct {
	Membership MembershipSnapshot
	Tasks      TaskSnapshot
}
