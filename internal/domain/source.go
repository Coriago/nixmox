package domain

import "time"

type Repository struct {
	SchemaVersion    int                         `json:"schema_version"`
	ID               string                      `json:"id"`
	Version          int64                       `json:"version"`
	Name             string                      `json:"name"`
	Remote           RepositoryRemote            `json:"remote"`
	Upstream         RepositoryUpstream          `json:"upstream"`
	Refs             []SourceRef                 `json:"refs"`
	Configurations   []Configuration             `json:"configurations"`
	Artifacts        []string                    `json:"artifact_ids"`
	AcceptedCommands map[string]SourceAcceptance `json:"accepted_commands"`
}
type RepositoryRemote struct {
	URL           string  `json:"url"`
	CredentialRef *string `json:"credential_ref"`
}
type RepositoryUpstream struct {
	Branch    string     `json:"branch"`
	Commit    *string    `json:"commit"`
	FetchedAt *time.Time `json:"fetched_at"`
}
type SourceRef struct {
	ID            string            `json:"id"`
	RepositoryID  string            `json:"repository_id"`
	Kind          string            `json:"kind"`
	Name          string            `json:"name"`
	Head          string            `json:"head"`
	UpstreamRefID *string           `json:"upstream_ref_id"`
	Ahead         int               `json:"ahead"`
	Behind        int               `json:"behind"`
	Publication   SourcePublication `json:"publication"`
}
type SourcePublication struct {
	State          string  `json:"state"`
	RemoteBranch   *string `json:"remote_branch"`
	PullRequestURL *string `json:"pull_request_url"`
}
type SourceAcceptance struct {
	Commit          string `json:"commit"`
	RefID           string `json:"ref_id"`
	ArtifactID      string `json:"artifact_id,omitempty"`
	ConfigurationID string `json:"configuration_id,omitempty"`
}
type Source struct {
	RepositoryID   string `json:"repository_id"`
	Configuration  string `json:"configuration"`
	RefID          string `json:"ref_id"`
	Commit         string `json:"commit"`
	FlakeDirectory string `json:"flake_directory"`
	FlakeAttribute string `json:"flake_attribute"`
}
type Configuration struct {
	SchemaVersion        int                 `json:"schema_version"`
	ID                   string              `json:"id"`
	Version              int64               `json:"version"`
	RepositoryID         string              `json:"repository_id"`
	Name                 string              `json:"name"`
	FlakeDirectory       string              `json:"flake_directory"`
	FlakeAttribute       string              `json:"flake_attribute"`
	RefID                string              `json:"ref_id"`
	Readiness            string              `json:"readiness"`
	InspectedCommit      string              `json:"inspected_commit"`
	PreparationTemplates map[string][]string `json:"preparation_templates,omitempty"`
}
type Artifact struct {
	SchemaVersion    int      `json:"schema_version"`
	ID               string   `json:"id"`
	Version          int64    `json:"version"`
	RepositoryID     string   `json:"repository_id"`
	CommandID        string   `json:"command_id"`
	Commit           string   `json:"commit"`
	RemoteBaseCommit string   `json:"remote_base_commit"`
	Prerequisites    []string `json:"prerequisites"`
	Bundle           struct {
		ObjectKey string `json:"object_key"`
		SHA256    string `json:"sha256"`
		Bytes     int64  `json:"bytes"`
	} `json:"bundle"`
	Storage struct {
		State string `json:"state"`
	} `json:"storage"`
	Retention struct {
		State           string   `json:"state"`
		PublishedCommit *string  `json:"published_commit"`
		Pins            []string `json:"pins"`
	} `json:"retention"`
	ValidationID  *string  `json:"validation_id"`
	TaskIDs       []string `json:"task_ids"`
	PublicationID *string  `json:"publication_id"`
}
