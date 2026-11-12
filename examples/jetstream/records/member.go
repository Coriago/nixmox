package records

import (
	"context"
	"errors"
	"github.com/nats-io/nats.go/jetstream"
)

type Role string

const (
	Pending    Role = "pending"
	Full       Role = "full"
	FullStable Role = "full-stable"
)

type EvictStatus string

const (
	EvictPending   EvictStatus = "pending"
	EvictRunning   EvictStatus = "running"
	EvictAttempted EvictStatus = "attempted"
	EvictCompleted EvictStatus = "completed"
	EvictFailed    EvictStatus = "failed"
)

// MemberRecord is assembled for reading; it is never stored as one KV value.
type MemberRecord struct {
	Desired MemberDesired `json:"desired"`
	State   MemberState   `json:"state"`
}

type MemberDesired struct {
	Membership MembershipDesired `json:"membership"`
	Primary    PrimaryDesired    `json:"primary"`
	Bootstrap  BootstrapDesired  `json:"bootstrap"`
}

// MEMBERS/<host>.desired.membership; membershipTaskController owns updates.
type MembershipDesired struct {
	Revision     uint64 `json:"-"` // KV entry revision; never persisted in the value.
	HostID       string `json:"hostId"`
	Role         Role   `json:"role"`
	TransitionID string `json:"transitionId,omitempty"`
}

// MEMBERS/<host>.desired.primary; membershipTaskController owns updates.
type PrimaryDesired struct {
	Revision uint64 `json:"-"` // KV entry revision; never persisted in the value.
	Enabled  bool   `json:"enabled"`
	Evict    bool   `json:"evict"`
}

// MEMBERS/<host>.desired.bootstrap; bootstrapController owns updates.
type BootstrapDesired struct {
	Revision uint64 `json:"-"` // KV entry revision; never persisted in the value.
	Enabled  bool   `json:"enabled"`
}

type MemberState struct {
	Membership MembershipState `json:"membership"`
	Primary    ServerState     `json:"primary"`
	Bootstrap  ServerState     `json:"bootstrap"`
}

// MEMBERS/<host>.state.membership; membershipTaskController owns updates.
type MembershipState struct {
	Revision uint64 `json:"-"` // KV entry revision; never persisted in the value.
	Role     Role   `json:"role"`
}

// MEMBERS/<host>.state.primary or .state.bootstrap; natsServiceController owns updates.
type ServerState struct {
	Revision          uint64      `json:"-"` // KV entry revision; never persisted in the value.
	Status            string      `json:"status"`
	Name              string      `json:"name,omitempty"`
	RouteURL          string      `json:"routeUrl,omitempty"`
	Replicas          int         `json:"replicas,omitempty"`
	Endpoints         []string    `json:"endpoints,omitempty"`
	Evict             EvictStatus `json:"evict,omitempty"`
	EvictOperationID  string      `json:"evictOperationId,omitempty"`
	RemainingReplicas int         `json:"remainingReplicas"`
	Error             string      `json:"error,omitempty"`
}

// Host-bound record IO, independent of stores/caches/controllers.
// Inject KV after bucket Create/Open. Controllers get narrow writer interfaces.
// Updates use value.Revision for CAS; zero means create-only. JSON omits Revision.
type MemberWriter struct {
	HostID string
	KV     jetstream.KeyValue
}

func (w *MemberWriter) CreateMembership(ctx context.Context, member MemberRecord) error {
	// Used by both cluster creation and enrollment for the local host.
	// Create six keys: desired/state each have membership, primary and bootstrap.
	// hostId lives in desired.membership; eviction intent lives in desired.primary.
	// Fail on conflicting existing sections; partial creation is not an atomic record.
	return errors.ErrUnsupported
}

func (w *MemberWriter) UpdateMembershipDesired(ctx context.Context, value MembershipDesired) error {
	// Validate value.HostID == local HostID; replace local desired.membership with CAS.
	return errors.ErrUnsupported
}

func (w *MemberWriter) UpdatePrimaryDesired(ctx context.Context, value PrimaryDesired) error {
	// Replace local desired.primary with CAS; membership tasks own this intent.
	return errors.ErrUnsupported
}

func (w *MemberWriter) UpdateBootstrapDesired(ctx context.Context, value BootstrapDesired) error {
	// Replace local desired.bootstrap with CAS; no arbitrary caller-supplied key.
	return errors.ErrUnsupported
}

func (w *MemberWriter) UpdateMembershipState(ctx context.Context, value MembershipState) error {
	// Replace local state.membership with CAS.
	return errors.ErrUnsupported
}

func (w *MemberWriter) UpdatePrimaryState(ctx context.Context, value ServerState) error {
	// Replace local state.primary using value.Revision for CAS.
	return errors.ErrUnsupported
}

func (w *MemberWriter) UpdateBootstrapState(ctx context.Context, value ServerState) error {
	// Replace local state.bootstrap using value.Revision for CAS.
	return errors.ErrUnsupported
}
