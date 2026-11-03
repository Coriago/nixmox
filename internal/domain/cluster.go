package domain

import "time"

type Cluster struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	CreatedByNodeID string    `json:"createdByNodeId"`
	CA              ClusterCA `json:"ca"`
}

type ClusterCA struct {
	Certificate  string `json:"certificate"`
	CredentialID string `json:"credentialId"`
}

type Member struct {
	EnrollmentRequestID  string     `json:"enrollmentRequestId,omitempty"`
	NodeID               string     `json:"nodeId"`
	Name                 string     `json:"name"`
	ClusterID            string     `json:"clusterId"`
	Status               string     `json:"status"`
	Environment          string     `json:"environment"`
	AgePublicKey         string     `json:"agePublicKey"`
	ConfigurationID      string     `json:"configurationId,omitempty"`
	ConfigurationVersion uint64     `json:"configurationVersion,omitempty"`
	Daemon               DaemonInfo `json:"daemon"`
	Endpoints            []Endpoint `json:"endpoints"`
}

type DaemonInfo struct {
	Version          string    `json:"version"`
	StartedAt        time.Time `json:"startedAt"`
	UpdateReady      bool      `json:"updateReady"`
	AvailableVersion string    `json:"availableVersion,omitempty"`
}

type Endpoint struct {
	Network    string `json:"network"`
	Interface  string `json:"interface,omitempty"`
	HTTP       string `json:"http"`
	NATSClient string `json:"natsClient,omitempty"`
	NATSRoute  string `json:"natsRoute,omitempty"`
}

type Health struct {
	NodeID    string           `json:"nodeId"`
	Status    string           `json:"status"`
	CheckedAt time.Time        `json:"checkedAt"`
	Checks    map[string]Check `json:"checks"`
}

type Check struct {
	Status string `json:"status"`
}
