package domain

import "time"

type CacheStatus struct {
	NodeID          ID          `json:"node_id"`
	Version         Version     `json:"version"`
	ObservedAt      time.Time   `json:"observed_at"`
	LocalURL        string      `json:"local_url"`
	GatewayState    string      `json:"gateway_state"`
	Serving         bool        `json:"serving"`
	ServingReason   *string     `json:"serving_reason"`
	ActiveTransfers int         `json:"active_transfers"`
	Peers           []CachePeer `json:"peers"`
}

type CachePeer struct {
	NodeID     ID         `json:"node_id"`
	Endpoints  []Endpoint `json:"endpoints"`
	PublicKey  *string    `json:"public_key"`
	Available  bool       `json:"available"`
	ObservedAt *time.Time `json:"observed_at"`
}

type CacheLocation struct {
	NodeID      ID       `json:"node_id"`
	URL         string   `json:"url"`
	StorePath   string   `json:"store_path"`
	NARHash     Digest   `json:"nar_hash"`
	FileHash    Digest   `json:"file_hash"`
	NARSize     uint64   `json:"nar_size"`
	FileSize    uint64   `json:"file_size"`
	Compression string   `json:"compression"`
	Signatures  []string `json:"signatures"`
}

type StoreOutput struct {
	StorePath  string   `json:"store_path"`
	NARHash    Digest   `json:"nar_hash"`
	NARSize    uint64   `json:"nar_size"`
	References []string `json:"references"`
	Signatures []string `json:"signatures"`
	Deriver    *string  `json:"deriver"`
}
