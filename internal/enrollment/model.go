// Package enrollment models the contact-owned certificate exchange. Joining
// nodes never write authoritative membership records.
package enrollment

import (
	"nixmox/internal/domain"
	"time"
)

type Input struct {
	DiscoveryID   string `json:"discoveryId"`
	Address       string `json:"address"`
	ContactHostID string `json:"contactHostId"`
}

type Contact struct {
	NodeID   string `json:"nodeId"`
	Endpoint string `json:"endpoint"`
}

type Cluster struct {
	ID                    string   `json:"id"`
	CAPublicKeyThumbprint string   `json:"caPublicKeyThumbprint"`
	CACertificate         string   `json:"caCertificate"`
	NATSEndpoints         []string `json:"natsEndpoints"`
	ContactNode           Contact  `json:"contactNode"`
}

type JoiningNode struct {
	CandidateEndpoints  []string          `json:"candidateEndpoints,omitempty"`
	NodeID              string            `json:"nodeId"`
	Name                string            `json:"name"`
	Endpoint            string            `json:"endpoint"`
	PublicKeyThumbprint string            `json:"publicKeyThumbprint"`
	CSR                 string            `json:"csr"`
	IssuedCertificate   string            `json:"issuedCertificate"`
	DiscoveryID         string            `json:"discoveryId,omitempty"`
	Environment         string            `json:"environment"`
	AgePublicKey        string            `json:"agePublicKey"`
	Daemon              domain.DaemonInfo `json:"daemon"`
}

type Decision struct {
	Result    string    `json:"result"`
	ByNodeID  string    `json:"byNodeId"`
	DecidedAt time.Time `json:"decidedAt"`
}

type Request struct {
	ID          string      `json:"id"`
	Status      string      `json:"status"`
	CreatedAt   time.Time   `json:"createdAt"`
	JoiningNode JoiningNode `json:"joiningNode"`
	Cluster     Cluster     `json:"cluster"`
	Initiator   string      `json:"initiator"`
	Decision    *Decision   `json:"decision"`
	Error       string      `json:"error,omitempty"`
}

type Confirmation struct {
	RequestID   string `json:"requestId"`
	Certificate string `json:"certificate"`
}
