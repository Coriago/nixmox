package views

import (
	"time"

	"nixmox/internal/domain"
	logs "nixmox/internal/ui/components/log"
	"nixmox/internal/ui/components/telemetry"
)

// Observations are supplied by the backend adapter, never synthesized by views.
type Cluster struct{ ID, Name, Gateway string }

type Host struct {
	Source                                            domain.Host
	ID, Name, Address, Reachability, Membership, Role string
	CPU                                               *float64
	Memory                                            string
	Architecture, Processor, OS, Uptime               string
	APIEndpoint, NATSLeaf, DiskIO, ObservedAt         string
	Metrics                                           []telemetry.Model
}

type Task struct {
	Source                                          domain.Task
	ID, Name, HostID, State, Phase, Result, Started string
	OwnerID, CommandID, Scope, ProgressUnit         string
	Done, Total                                     int
	Age                                             time.Duration
	Log                                             logs.Model
}

// Discovery is an observation, distinct from enrollment and membership.
type Discovery struct {
	NodeID                                   domain.ID
	RequestID                                domain.ID
	Version                                  domain.Version
	ExpiresAt                                time.Time
	ID, Name, Address, Fingerprint, Decision string
	Requested                                bool
}
