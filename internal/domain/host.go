package domain

import "time"

// HostSample is owner-collected evidence. Membership and desired configuration
// remain separate, quorum-owned records and are never inferred from this sample.
type HostSample struct {
	SchemaVersion int              `json:"schema_version"`
	HostID        string           `json:"host_id"`
	Name          string           `json:"name"`
	System        string           `json:"system"`
	Environment   string           `json:"environment"`
	ObservedAt    time.Time        `json:"observed_at"`
	Freshness     string           `json:"freshness"`
	RunningOutput *string          `json:"running_output"`
	Health        Health           `json:"health"`
	Endpoints     []Endpoint       `json:"endpoints"`
	Capabilities  HostCapabilities `json:"capabilities"`
}
type HostCapabilities struct {
	Build      bool `json:"build"`
	Install    bool `json:"install"`
	MediaWrite bool `json:"media_write"`
	Apply      bool `json:"apply"`
	CacheServe bool `json:"cache_serve"`
}
type HostMembership struct {
	NodeID     string `json:"node_id"`
	Version    int64  `json:"version"`
	ClusterID  string `json:"cluster_id"`
	State      string `json:"state"`
	Epoch      int    `json:"membership_epoch"`
	BrokerRole string `json:"broker_role"`
}
type HostBinding struct {
	RepositoryID string `json:"repository_id"`
	Name         string `json:"name"`
	RefID        string `json:"ref_id"`
}
type Host struct {
	SchemaVersion      int              `json:"schema_version"`
	ID                 string           `json:"id"`
	Version            int64            `json:"version"`
	Name               string           `json:"name"`
	ClusterID          *string          `json:"cluster_id"`
	Membership         string           `json:"membership"`
	MembershipEpoch    int              `json:"membership_epoch"`
	Environment        string           `json:"environment"`
	Reachability       string           `json:"reachability"`
	Health             Health           `json:"health"`
	LastSeen           *time.Time       `json:"last_seen"`
	Configuration      *HostBinding     `json:"configuration"`
	DeploymentVerified bool             `json:"deployment_verified"`
	PendingReasons     []string         `json:"pending_reasons"`
	Endpoints          []Endpoint       `json:"endpoints"`
	Capabilities       HostCapabilities `json:"capabilities"`
	BrokerRole         string           `json:"broker_role"`
	ActiveTaskIDs      []string         `json:"active_task_ids"`
	Connectivity       []Connectivity   `json:"connectivity"`
	System             string           `json:"system"`
	RunningOutput      *string          `json:"running_output"`
}
type Connectivity struct {
	PeerID          string  `json:"peer_id"`
	Service         string  `json:"service"`
	SelectedAddress *string `json:"selected_address"`
	State           string  `json:"state"`
	Reason          *string `json:"reason"`
}
type HostObservation struct {
	Epoch  int        `json:"membership_epoch"`
	Sample HostSample `json:"sample"`
}
