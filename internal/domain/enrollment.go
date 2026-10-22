package domain

import "time"

type EnrollmentRequest struct {
	ID                  string `json:"id"`
	NodeID              string `json:"node_id"`
	Name                string `json:"name"`
	ExpectedClusterName string `json:"expected_cluster_name"`
	DaemonCSR           string `json:"daemon_csr"`
	BrokerCSR           string `json:"broker_csr"`
}
type JoinRequest struct {
	ID                 string    `json:"id"`
	Version            int64     `json:"version"`
	NodeID             string    `json:"node_id"`
	Name               string    `json:"name"`
	ObservedAddress    *string   `json:"observed_address"`
	KeyFingerprint     string    `json:"key_fingerprint"`
	State              string    `json:"state"`
	ExpiresAt          time.Time `json:"expires_at"`
	ClusterID          string    `json:"cluster_id"`
	ClusterFingerprint string    `json:"cluster_fingerprint"`
	Decision           *string   `json:"decision"`
}
type Endpoint struct {
	Service   string `json:"service"`
	Transport string `json:"transport"`
	Address   string `json:"address"`
}
type Issuance struct {
	RequestID               string     `json:"request_id"`
	ClusterID               string     `json:"cluster_id"`
	ClusterName             string     `json:"cluster_name"`
	NodeID                  string     `json:"node_id"`
	Epoch                   int        `json:"membership_epoch"`
	CA                      string     `json:"ca"`
	DaemonCertificate       string     `json:"daemon_certificate"`
	BrokerCertificate       string     `json:"broker_certificate"`
	IssuerNodeID            string     `json:"issuer_node_id"`
	IssuerEpoch             int        `json:"issuer_epoch"`
	IssuerBrokerCertificate string     `json:"issuer_broker_certificate"`
	Endpoints               []Endpoint `json:"endpoints"`
}
