package domain

import "time"

type ClusterStatus struct {
	ClusterID    *string   `json:"cluster_id"`
	Name         *string   `json:"name"`
	Membership   string    `json:"membership"`
	Quorum       bool      `json:"quorum"`
	FullMembers  int       `json:"full_members"`
	BrokerRole   string    `json:"broker_role"`
	IssuerNodeID *string   `json:"issuer_node_id"`
	ObservedAt   time.Time `json:"observed_at"`
	Freshness    string    `json:"freshness"`
}
