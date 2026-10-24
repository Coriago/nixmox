package domain

// EnrollmentSubmission carries public proofs only. The service assigns versions,
// expiration, decisions and privileges; the applicant cannot choose them.
type EnrollmentSubmission struct {
	Signature           string `json:"signature"`
	RequestID           ID     `json:"request_id"`
	NodeID              ID     `json:"node_id"`
	ExpectedClusterName string `json:"expected_cluster_name"`
	DaemonCSR           string `json:"daemon_csr"`
	BrokerCSR           string `json:"broker_csr"`
}

type EnrollmentStatus struct {
	Topology           *SignedTopology `json:"topology,omitempty"`
	Request            JoinRequest     `json:"request"`
	Cluster            Cluster         `json:"cluster"`
	ClusterFingerprint Digest          `json:"cluster_fingerprint"`
	BrokerKey          Digest          `json:"broker_key"`
	LeafAddress        string          `json:"leaf_address"`
}

// ClusterTrust is the durable local association. Its public material is safe to
// exchange over enrollment HTTP; neither machine nor issuer private keys appear.
type ClusterTrust struct {
	BrokerTopology     *SignedTopology `json:"broker_topology,omitempty"` // Last applied local transport, separate from staged topology.
	BrokerDirectory    string          `json:"broker_directory,omitempty"`
	BrokerTransition   string          `json:"broker_transition,omitempty"`
	Topology           *SignedTopology `json:"topology,omitempty"`
	AuthorityAddress   string          `json:"authority_address"`
	ControlInitialized bool            `json:"control_initialized"`
	CommandID          ID              `json:"command_id"`
	Cluster            Cluster         `json:"cluster"`
	Issuance           Issuance        `json:"issuance"`
	BrokerKey          Digest          `json:"broker_key"`
	LeafAddress        string          `json:"leaf_address"`
}

type Member struct {
	Configuration     *Binding       `json:"configuration,omitempty"`
	Assignments       map[ID]Version `json:"assignments,omitempty"`
	TopologyCommand   ID             `json:"topology_command,omitempty"`
	Full              bool           `json:"full"`
	ForcedBy          ID             `json:"forced_by,omitempty"`
	Version           Version        `json:"version"`
	Removed           bool           `json:"removed"`
	BrokerCertificate Digest         `json:"broker_certificate"`
	NodeID            ID             `json:"node_id"`
	Epoch             uint64         `json:"epoch"`
	DaemonKey         Digest         `json:"daemon_key"`
	BrokerKey         Digest         `json:"broker_key"`
	RequestID         ID             `json:"request_id"`
}

type MembershipState struct {
	Topology *SignedTopology      `json:"topology,omitempty"`
	Power    *PowerReservation    `json:"power,omitempty"`
	Removals map[ID]SignedRemoval `json:"removals"`
	Cluster  Cluster              `json:"cluster"`
	Members  map[ID]Member        `json:"members"`
	Requests map[ID]JoinRequest   `json:"requests"`
}

type Heartbeat struct {
	BootID          string  `json:"boot_id,omitempty"`
	TopologyVersion Version `json:"topology_version"`
	Host            Host    `json:"host"`
	StreamID        ID      `json:"stream_id"`
}
