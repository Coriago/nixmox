package domain

import "time"

type CacheMetrics struct {
	Lookups, LookupNanoseconds, PeerFailures, Failures, Cancellations uint64
	Active, BytesExported, BytesReceived                              int64
}

type CachePeer struct {
	NodeID string `json:"node_id"`
	State  string `json:"state"`
}
type CacheStatus struct {
	NodeID               string      `json:"node_id"`
	Version              int64       `json:"version"`
	SettingsVersion      int64       `json:"settings_version"`
	ObservedAt           time.Time   `json:"observed_at"`
	LocalURL             string      `json:"local_url"`
	GatewayState         string      `json:"gateway_state"`
	Serving              bool        `json:"serving"`
	ServingReason        *string     `json:"serving_reason"`
	SigningCredentialRef *string     `json:"signing_credential_ref"`
	Declared             bool        `json:"declared"`
	ActiveTransfers      int64       `json:"active_transfers"`
	BytesExported        int64       `json:"bytes_exported"`
	BytesReceived        int64       `json:"bytes_received"`
	Failures             uint64      `json:"failures"`
	Peers                []CachePeer `json:"peers"`
}
type CacheLocation struct {
	NodeID     string  `json:"node_id"`
	State      string  `json:"state"`
	OutputPath string  `json:"output_path"`
	NarHash    *string `json:"nar_hash"`
	NarSize    *uint64 `json:"nar_size"`
}
