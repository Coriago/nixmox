package domain

import "time"

// Source pins either a Git commit or a recipe version, never a moving latest head.
// Recipe realization is not implemented; Git materialization remains Git-only.
type Source struct {
	RepositoryID   ID         `json:"repository_id"`
	Configuration  string     `json:"configuration"`
	RefID          *ID        `json:"ref_id"`
	Commit         Commit     `json:"commit,omitempty"`
	Recipe         *RecipePin `json:"recipe,omitempty"`
	FlakeDirectory string     `json:"flake_directory"`
	FlakeAttribute string     `json:"flake_attribute"`
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

// RepositoryState is the single authoritative CAS unit for settings and refs.
// Applied commands retain small acceptance proofs for uncertain-write recovery.
type RepositoryState struct {
	Repository Repository              `json:"repository"`
	Refs       []Reference             `json:"refs"`
	Applied    map[ID]RepositoryResult `json:"applied"`
}

type RepositoryResult struct {
	RepositoryID ID      `json:"repository_id"`
	Version      Version `json:"version"`
	Commit       *Commit `json:"commit"`
}

type Remote struct {
	Username      string `json:"username,omitempty"`
	HostKey       string `json:"host_key,omitempty"`
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

// StoredObject identifies a checksummed payload; it does not prescribe a Git format.
type StoredObject struct {
	ObjectKey string `json:"object_key"`
	SHA256    Digest `json:"sha256"`
	Bytes     int64  `json:"bytes"`
}

type Publication struct {
	Record
	RemoteURL       string     `json:"remote_url"`
	CredentialRef   *ID        `json:"credential_ref"`
	RepositoryID    ID         `json:"repository_id"`
	RefID           ID         `json:"ref_id,omitempty"`
	Recipe          *RecipePin `json:"recipe,omitempty"`
	Commit          Commit     `json:"commit"`
	RemoteBranch    string     `json:"remote_branch"`
	PullRequestURL  *string    `json:"pull_request_url"`
	RemoteHead      *Commit    `json:"remote_head"`
	IntegrationBase Commit     `json:"integration_base"`
	State           string     `json:"state"`
	CreatedAt       time.Time  `json:"created_at"`
}

type GCPlan struct {
	Record
	RepositoryID              ID              `json:"repository_id"`
	RecipeVersions            []RecipeVersion `json:"recipe_versions"`
	ReclaimedBytes            int64           `json:"reclaimed_bytes"`
	ProtectedReferences       []string        `json:"protected_references"`
	UnpublishedRecipeVersions []RecipeVersion `json:"unpublished_recipe_versions"`
	Review                    Review          `json:"review"`
	ExpectedVersions          Versions        `json:"expected_versions"`
}

type FileManifest struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 Digest `json:"sha256"`
}
