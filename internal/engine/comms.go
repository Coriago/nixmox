package engine

import (
	"encoding/json"

	"nixmox/internal/domain"
)

type Query struct {
	CallerID        domain.ID
	MembershipEpoch uint64
	ID              domain.ID
	Type            string
	TargetID        domain.ID
	Payload         json.RawMessage
}
type SecretIngress struct {
	CommandID  domain.ID
	HolderID   domain.ID
	Name, Kind string
}
