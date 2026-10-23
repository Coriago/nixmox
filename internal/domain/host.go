package domain

import "time"

type Identity struct {
	SchemaVersion      int     `json:"schema_version"`
	Version            Version `json:"version"`
	NodeID             ID      `json:"node_id"`
	KeyID              ID      `json:"key_id"`
	PublicKey          string  `json:"public_key"`
	ClusterID          *ID     `json:"cluster_id"`
	MembershipEpoch    *uint64 `json:"membership_epoch"`
	ClusterFingerprint *Digest `json:"cluster_fingerprint"`
}

type Endpoint struct {
	Service   string `json:"service"`
	Transport string `json:"transport"`
	Address   string `json:"address"`
	Interface string `json:"interface"`
	Priority  int    `json:"priority"`
	Verified  bool   `json:"verified"`
}

type Connectivity struct {
	PeerID          ID      `json:"peer_id"`
	Service         string  `json:"service"`
	SelectedAddress *string `json:"selected_address"`
	State           string  `json:"state"`
	Reason          *string `json:"reason"`
}

type HealthReason struct {
	Code     string `json:"code"`
	Resource string `json:"resource"`
	Message  string `json:"message"`
}

type Health struct {
	Status     string         `json:"status"`
	ObservedAt *time.Time     `json:"observed_at"`
	Reasons    []HealthReason `json:"reasons"`
}

type Capabilities struct {
	Build      bool `json:"build"`
	Install    bool `json:"install"`
	MediaWrite bool `json:"media_write"`
	Power      bool `json:"power"`
}

type Host struct {
	Record
	Name               string         `json:"name"`
	ClusterID          *ID            `json:"cluster_id"`
	Membership         Membership     `json:"membership"`
	MembershipEpoch    *uint64        `json:"membership_epoch"`
	Environment        Environment    `json:"environment"`
	Reachability       Reachability   `json:"reachability"`
	Health             Health         `json:"health"`
	LastSeen           *time.Time     `json:"last_seen"`
	Configuration      *Binding       `json:"configuration"`
	DeploymentVerified bool           `json:"deployment_verified"`
	PendingReasons     []string       `json:"pending_reasons"`
	Endpoints          []Endpoint     `json:"endpoints"`
	Connectivity       []Connectivity `json:"connectivity"`
	Capabilities       Capabilities   `json:"capabilities"`
	BrokerRole         string         `json:"broker_role"`
	ActiveTaskIDs      []ID           `json:"active_task_ids"`
}

type Binding struct {
	RepositoryID ID     `json:"repository_id"`
	Name         string `json:"name"`
	RefID        ID     `json:"ref_id"`
}

type Quorum struct {
	Available bool `json:"available"`
	Replicas  int  `json:"replicas"`
	Required  int  `json:"required"`
}

type StreamReplication struct {
	Replicas int  `json:"replicas"`
	Current  int  `json:"current"`
	Ready    bool `json:"ready"`
}
type BrokerStatus struct {
	Mode           string                       `json:"mode"`
	MetadataPeers  int                          `json:"metadata_peers"`
	MetadataLeader string                       `json:"metadata_leader"`
	Streams        map[string]StreamReplication `json:"streams"`
}
type ClusterStatus struct {
	Broker BrokerStatus `json:"broker"`
	Observed
	ClusterID  *ID        `json:"cluster_id"`
	Membership Membership `json:"membership"`
	Health     Health     `json:"health"`
	Quorum     Quorum     `json:"quorum"`
}

type Cluster struct {
	Record
	Name             string     `json:"name"`
	GatewayEndpoints []Endpoint `json:"gateway_endpoints"`
	Membership       Membership `json:"membership"`
	Health           Health     `json:"health"`
	IssuerNodeID     ID         `json:"issuer_node_id"`
	ReplicaNodeIDs   []ID       `json:"replica_node_ids"`
}

type Discovery struct {
	ID             ID         `json:"id"`
	Kind           string     `json:"kind"`
	NodeID         *ID        `json:"node_id"`
	ClusterID      *ID        `json:"cluster_id"`
	Name           string     `json:"name"`
	Endpoints      []Endpoint `json:"endpoints"`
	KeyFingerprint Digest     `json:"key_fingerprint"`
	ExpiresAt      time.Time  `json:"expires_at"`
}

type JoinRequest struct {
	// Diagnostic origin only; not part of signed proofs or authorization.
	Trace     TraceContext `json:"trace,omitempty"`
	Signature string       `json:"signature"`
	Record
	RequestID           ID            `json:"request_id"`
	NodeID              ID            `json:"node_id"`
	ExpectedClusterName string        `json:"expected_cluster_name"`
	DaemonCSR           string        `json:"daemon_csr"`
	BrokerCSR           string        `json:"broker_csr"`
	KeyFingerprint      Digest        `json:"key_fingerprint"`
	State               string        `json:"state"`
	ExpiresAt           time.Time     `json:"expires_at"`
	Decision            *JoinDecision `json:"decision"`
}

type JoinDecision struct {
	Approved       bool      `json:"approved"`
	RequestVersion Version   `json:"request_version"`
	DecidedAt      time.Time `json:"decided_at"`
	IssuanceID     *ID       `json:"issuance_id"`
}

type Issuance struct {
	ID                 ID     `json:"id"`
	RequestID          ID     `json:"request_id"`
	ClusterID          ID     `json:"cluster_id"`
	MembershipEpoch    uint64 `json:"membership_epoch"`
	DaemonCertificate  string `json:"daemon_certificate"`
	BrokerCertificate  string `json:"broker_certificate"`
	ClusterCA          string `json:"cluster_ca"`
	ClusterFingerprint Digest `json:"cluster_fingerprint"`
	Grants             Grants `json:"grants"`
}

type Grants struct {
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
}

type Removal struct {
	ClusterID       ID      `json:"cluster_id"`
	NodeID          ID      `json:"node_id"`
	MembershipEpoch uint64  `json:"membership_epoch"`
	Status          string  `json:"status"`
	RemovalVersion  Version `json:"removal_version"`
}

type SignedRemoval struct {
	Record    Removal `json:"record"`
	Signature string  `json:"signature"`
}

type CredentialScope struct {
	RepositoryIDs []ID `json:"repository_ids"`
}

type CredentialReplica struct {
	NodeID ID     `json:"node_id"`
	State  string `json:"state"`
	Source string `json:"source"`
}

type Credential struct {
	Record
	Name     string              `json:"name"`
	Kind     string              `json:"kind"`
	Scope    CredentialScope     `json:"scope"`
	Replicas []CredentialReplica `json:"replicas"`
	Value    *struct{}           `json:"value"`
}

type CredentialCheck struct {
	Kind   string          `json:"kind"`
	Checks []Check         `json:"checks"`
	Scope  CredentialScope `json:"scope"`
	Passed bool            `json:"passed"`
}

type Check struct {
	Name   string  `json:"name"`
	Passed bool    `json:"passed"`
	Reason *string `json:"reason"`
}

type SecretStaging struct {
	Reference   ID        `json:"reference"`
	OwnerNodeID ID        `json:"owner_node_id"`
	ExpiresAt   time.Time `json:"expires_at"`
	CommandID   ID        `json:"command_id"`
}

type NetworkStatus struct {
	NodeID       ID                `json:"node_id"`
	Environment  Environment       `json:"environment"`
	Network      NetworkConnection `json:"network"`
	WiFiNetworks []WiFiNetwork     `json:"wifi_networks"`
	ConsoleURL   *string           `json:"console_url"`
	NextScreen   string            `json:"next_screen"`
}

type NetworkConnection struct {
	Interface *string   `json:"interface"`
	Link      string    `json:"link"`
	Address   *string   `json:"address"`
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
}

type WiFiNetwork struct {
	ID            ID     `json:"id"`
	SSID          string `json:"ssid"`
	SignalPercent *int   `json:"signal_percent"`
	Security      string `json:"security"`
}
