package domain

import "time"

// HostSample is owner-collected evidence. Membership and desired configuration
// remain separate, quorum-owned records and are never inferred from this sample.
type HostSample struct {
	SchemaVersion int        `json:"schema_version"`
	HostID        string     `json:"host_id"`
	Name          string     `json:"name"`
	System        string     `json:"system"`
	Environment   string     `json:"environment"`
	ObservedAt    time.Time  `json:"observed_at"`
	Freshness     string     `json:"freshness"`
	RunningOutput *string    `json:"running_output"`
	Health        Health     `json:"health"`
	Endpoints     []Endpoint `json:"endpoints"`
}
