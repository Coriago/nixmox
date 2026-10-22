package domain

import "time"

type ClusterStatus struct {
	ClusterID    *string   `json:"cluster_id"`
	Name         *string   `json:"name"`
	Membership   string    `json:"membership"`
	Quorum       Quorum    `json:"quorum"`
	Health       Health    `json:"health"`
	FullMembers  int       `json:"full_members"`
	BrokerRole   string    `json:"broker_role"`
	IssuerNodeID *string   `json:"issuer_node_id"`
	ObservedAt   time.Time `json:"observed_at"`
	Freshness    string    `json:"freshness"`
}

type Quorum struct {
	Available bool `json:"available"`
	Replicas  int  `json:"replicas"`
	Required  int  `json:"required"`
}
type Health struct {
	Status     string         `json:"status"`
	ObservedAt *time.Time     `json:"observed_at"`
	Reasons    []HealthReason `json:"reasons"`
}
type HealthReason struct {
	Code     string `json:"code"`
	Resource string `json:"resource"`
	Message  string `json:"message"`
}
