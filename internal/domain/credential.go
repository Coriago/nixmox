package domain

type CredentialReplica struct {
	NodeID string `json:"node_id"`
	State  string `json:"state"`
	Source string `json:"source"`
}
type CredentialScope struct {
	RepositoryIDs  []string `json:"repository_ids"`
	CacheNodeIDs   []string `json:"cache_node_ids"`
	UpdaterNodeIDs []string `json:"updater_node_ids"`
}
type Credential struct {
	ID               string              `json:"id"`
	Version          int64               `json:"version"`
	Name             string              `json:"name"`
	Kind             string              `json:"kind"`
	Scope            CredentialScope     `json:"scope"`
	Replicas         []CredentialReplica `json:"replicas"`
	PublicKey        *string             `json:"public_key"`
	Value            any                 `json:"value"`
	State            string              `json:"state"`
	RemovalCommandID string              `json:"removal_command_id,omitempty"`
}
type SecretImport struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	StorageNodeID string `json:"storage_node_id"`
}
type CredentialCheck struct {
	Kind      string   `json:"kind"`
	Checks    []string `json:"checks"`
	Scope     string   `json:"scope"`
	Passed    bool     `json:"passed"`
	PublicKey *string  `json:"public_key,omitempty"`
}
