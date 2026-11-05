// member.go defines separate desired/observed records and member reconciliation.
package main

import "context"

// Role is membership responsibility, independent of current availability.
type Role string

const (
	Unjoined     Role = "unjoined"
	Member       Role = "member"
	MemberStable Role = "member-stable"
)

// MemberDesired belongs at MEMBERS/<id>.desired.
// Primary intent is separate from role so demotion can prepare before retirement.
type MemberDesired struct {
	Role             Role   `json:"role"`
	PlannedTerminate bool   `json:"plannedTerminate"`
	Primary          bool   `json:"primary"`
	Bootstrap        bool   `json:"bootstrap"`
	Transition       string `json:"transition,omitempty"`
}

// MemberState belongs at MEMBERS/<id>.state and is written only by that daemon.
type MemberState struct {
	Role                    Role   `json:"role"`
	Online                  bool   `json:"online"`
	Primary                 bool   `json:"primary"`
	PrimaryReady            bool   `json:"primaryReady"`
	Bootstrap               bool   `json:"bootstrap"`
	BootstrapReady          bool   `json:"bootstrapReady"`
	ObservedDesiredRevision uint64 `json:"observedDesiredRevision"`
}

// MemberRecord is a logical view of two keys, not a combined persisted document.
type MemberRecord struct {
	ID      string
	Desired MemberDesired
	State   MemberState
}

// MemberController reconciles this daemon's local services and writes observed state.
type MemberController struct {
	Daemon *Daemon
}

var _ Controller = (*MemberController)(nil)

// Reconcile will apply local service intent and update only this member's state.
func (c *MemberController) Reconcile(ctx context.Context) error {
	return ErrNotImplemented
}

// ElectBootstrap will derive topology from desired roles and planned termination.
// Only the elected daemon changes its own desired bootstrap intent; this is
// intentionally separate from observed-state reconciliation.
func (c *MemberController) ElectBootstrap(ctx context.Context) error {
	return ErrNotImplemented
}
