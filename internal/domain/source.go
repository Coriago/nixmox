package domain

import "time"

type Source struct {
	RepositoryID   ID     `json:"repository_id"`
	Configuration  string `json:"configuration"`
	RefID          *ID    `json:"ref_id"`
	Commit         Commit `json:"commit"`
	FlakeDirectory string `json:"flake_directory"`
	FlakeAttribute string `json:"flake_attribute"`
}

type SourceSelector struct {
	RepositoryID  *ID     `json:"repository_id,omitempty"`
	Configuration *string `json:"configuration,omitempty"`
	RefID         *ID     `json:"ref_id,omitempty"`
	Commit        *Commit `json:"commit,omitempty"`
}

type Repository struct {
	Record
	Name     string   `json:"name"`
	Remote   Remote   `json:"remote"`
	Upstream Upstream `json:"upstream"`
}

type Remote struct {
	URL           string `json:"url"`
	CredentialRef *ID    `json:"credential_ref"`
}

type Upstream struct {
	Branch    string     `json:"branch"`
	Commit    *Commit    `json:"commit"`
	FetchedAt *time.Time `json:"fetched_at"`
}

type RepositoryRefs struct {
	Record
	RepositoryID ID          `json:"repository_id"`
	Refs         []Reference `json:"refs"`
}

type Reference struct {
	ID            ID             `json:"id"`
	RepositoryID  ID             `json:"repository_id"`
	Kind          string         `json:"kind"`
	Name          string         `json:"name"`
	Head          Commit         `json:"head"`
	UpstreamRefID *ID            `json:"upstream_ref_id"`
	Ahead         uint64         `json:"ahead"`
	Behind        uint64         `json:"behind"`
	Publication   RefPublication `json:"publication"`
}

type RefPublication struct {
	State          string  `json:"state"`
	RemoteBranch   *string `json:"remote_branch"`
	PullRequestURL *string `json:"pull_request_url"`
}

type Artifact struct {
	Record
	RepositoryID     ID                `json:"repository_id"`
	CommandID        ID                `json:"command_id"`
	Commit           Commit            `json:"commit"`
	RemoteBaseCommit Commit            `json:"remote_base_commit"`
	Prerequisites    []Commit          `json:"prerequisites"`
	Bundle           Bundle            `json:"bundle"`
	Storage          ArtifactStorage   `json:"storage"`
	Retention        ArtifactRetention `json:"retention"`
	ValidationID     ID                `json:"validation_id"`
	TaskIDs          []ID              `json:"task_ids"`
	PublicationID    *ID               `json:"publication_id"`
}

type Bundle struct {
	ObjectKey string `json:"object_key"`
	SHA256    Digest `json:"sha256"`
	Bytes     int64  `json:"bytes"`
}

type ArtifactStorage struct {
	State string `json:"state"`
}

type ArtifactRetention struct {
	State           string  `json:"state"`
	PublishedCommit *Commit `json:"published_commit"`
	Pins            []ID    `json:"pins"`
}

type Publication struct {
	Record
	RemoteURL       string    `json:"remote_url"`
	CredentialRef   *ID       `json:"credential_ref"`
	RepositoryID    ID        `json:"repository_id"`
	RefID           ID        `json:"ref_id"`
	Commit          Commit    `json:"commit"`
	RemoteBranch    string    `json:"remote_branch"`
	PullRequestURL  *string   `json:"pull_request_url"`
	RemoteHead      *Commit   `json:"remote_head"`
	IntegrationBase Commit    `json:"integration_base"`
	State           string    `json:"state"`
	CreatedAt       time.Time `json:"created_at"`
}

type GCPlan struct {
	Record
	RepositoryID           ID       `json:"repository_id"`
	ArtifactIDs            []ID     `json:"artifact_ids"`
	ReclaimedBytes         int64    `json:"reclaimed_bytes"`
	ProtectedReferences    []string `json:"protected_references"`
	UnpublishedArtifactIDs []ID     `json:"unpublished_artifact_ids"`
	Review                 Review   `json:"review"`
	ExpectedVersions       Versions `json:"expected_versions"`
}

type FileManifest struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 Digest `json:"sha256"`
}

type Resolution struct {
	Reference    ID             `json:"reference"`
	DraftID      ID             `json:"draft_id"`
	DraftVersion Version        `json:"draft_version"`
	OwnerNodeID  ID             `json:"owner_node_id"`
	Files        []FileManifest `json:"files"`
	ExpiresAt    time.Time      `json:"expires_at"`
}
