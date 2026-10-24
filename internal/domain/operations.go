package domain

// Optional fields use pointers when omission differs from an explicit value.
// Semantic validation and version checks belong to the operation handler.
type Empty struct {
}

type ClusterCreate struct {
	Name string `json:"name"`
}

type ClusterJoin struct {
	Address             string `json:"address"`
	ExpectedClusterName string `json:"expected_cluster_name"`
}

type JoinDecisionPayload struct {
	RequestID ID `json:"request_id"`
}

type HostAssign struct {
	HostID        ID     `json:"host_id"`
	RepositoryID  ID     `json:"repository_id"`
	Configuration string `json:"configuration"`
	RefID         *ID    `json:"ref_id,omitempty"`
}

type HostAdopt struct {
	HostID      ID     `json:"host_id"`
	CandidateID ID     `json:"candidate_id"`
	ConfirmHost string `json:"confirm_host"`
}

type ClusterLeave struct {
	HostID          ID     `json:"host_id"`
	MembershipEpoch uint64 `json:"membership_epoch"`
}

type HostRemove struct {
	HostID          ID     `json:"host_id"`
	Force           bool   `json:"force"`
	ConfirmHost     string `json:"confirm_host"`
	MembershipEpoch uint64 `json:"membership_epoch"`
}

type HostTarget struct {
	HostID ID `json:"host_id"`
}

type ForceFullMembership struct {
	HostID          ID     `json:"host_id"`
	MembershipEpoch uint64 `json:"membership_epoch"`
	ConfirmHost     string `json:"confirm_host"`
}

type HostPower struct {
	MembershipEpoch uint64 `json:"membership_epoch"`
	HostID          ID     `json:"host_id"`
	ConfirmHost     string `json:"confirm_host"`
}

type RepositoryImport struct {
	Username       string `json:"username,omitempty"`
	HostKey        string `json:"host_key,omitempty"`
	Name           string `json:"name"`
	RemoteURL      string `json:"remote_url"`
	UpstreamBranch string `json:"upstream_branch"`
	CredentialRef  *ID    `json:"credential_ref,omitempty"`
}

type RepositoryTarget struct {
	RepositoryID ID `json:"repository_id"`
}

type RepositoryRemoteSet struct {
	RepositoryID   ID      `json:"repository_id"`
	URL            string  `json:"url"`
	UpstreamBranch *string `json:"upstream_branch,omitempty"`
}

type RepositoryCredentialSet struct {
	Username     string `json:"username,omitempty"`
	HostKey      string `json:"host_key,omitempty"`
	RepositoryID ID     `json:"repository_id"`
	CredentialID ID     `json:"credential_ref"`
}

type TaskTarget struct {
	TaskID ID `json:"task_id"`
}

type DeploymentPreview struct {
	HostID ID              `json:"host_id"`
	Source *SourceSelector `json:"source,omitempty"`
}

type DeploymentBuild struct {
	HostID    ID              `json:"host_id"`
	Source    *SourceSelector `json:"source,omitempty"`
	BuilderID *ID             `json:"builder_id,omitempty"`
}

type DeploymentDiff struct {
	HostID               ID  `json:"host_id"`
	CandidateID          ID  `json:"candidate_id"`
	BaselineGenerationID *ID `json:"baseline_generation_id,omitempty"`
}

type DeploymentApply struct {
	Source      *SourceSelector `json:"source,omitempty"`
	CandidateID *ID             `json:"candidate_id,omitempty"`
	BuilderID   *ID             `json:"builder_id,omitempty"`
	Mode        ActivationMode  `json:"mode,omitempty"`
	AutoApprove bool            `json:"auto_approve"`
}

type DeploymentApprove struct {
	TaskID      ID      `json:"task_id"`
	DiffID      ID      `json:"diff_id"`
	DiffVersion Version `json:"diff_version"`
}

type DeploymentRollback struct {
	GenerationID ID             `json:"generation_id"`
	Mode         ActivationMode `json:"mode,omitempty"`
}

type InstallerEntry struct {
	HostID           ID                `json:"host_id"`
	Method           string            `json:"method"`
	InstallerID      *ID               `json:"installer_id,omitempty"`
	TaskID           *ID               `json:"task_id,omitempty"`
	ReviewID         *ID               `json:"review_id,omitempty"`
	Acknowledgements *Acknowledgements `json:"acknowledgements,omitempty"`
}

type DeploymentInstall struct {
	HostID           ID                `json:"host_id"`
	Method           string            `json:"method,omitempty"`
	ConfigurationID  *ID               `json:"configuration_id,omitempty"`
	PreparationID    *ID               `json:"preparation_id,omitempty"`
	InstallerID      *ID               `json:"installer_id,omitempty"`
	BuilderID        *ID               `json:"builder_id,omitempty"`
	TaskID           *ID               `json:"task_id,omitempty"`
	ReviewID         *ID               `json:"review_id,omitempty"`
	Acknowledgements *Acknowledgements `json:"acknowledgements,omitempty"`
}

type CredentialImport struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	StorageNodeID ID     `json:"storage_node_id"`
	StagingRef    ID     `json:"staging_ref"`
}

type CredentialCheckPayload struct {
	CredentialID ID  `json:"credential_id"`
	RepositoryID *ID `json:"repository_id,omitempty"`
}

type CredentialTarget struct {
	CredentialID ID `json:"credential_id"`
}
