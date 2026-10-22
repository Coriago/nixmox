package domain

import "time"

// Optional fields use pointers when omission differs from an explicit value.
// Semantic validation and version checks belong to the operation handler.
type Empty struct {
}

type NetworkConnect struct {
	NetworkID     ID  `json:"network_id"`
	CredentialRef *ID `json:"credential_ref,omitempty"`
}

type IdentityReset struct {
	ConfirmHost string `json:"confirm_host"`
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

type ClusterAdd struct {
	DiscoveryID            ID     `json:"discovery_id"`
	ExpectedNodeID         ID     `json:"expected_node_id"`
	ExpectedKeyFingerprint Digest `json:"expected_key_fingerprint"`
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

type HostPower struct {
	HostID      ID     `json:"host_id"`
	ConfirmHost string `json:"confirm_host"`
}

type HostDiagnostics struct {
	HostID ID         `json:"host_id"`
	Since  *time.Time `json:"since,omitempty"`
	Until  *time.Time `json:"until,omitempty"`
}

type HostBootstrap struct {
	SSHTarget          string `json:"ssh_target"`
	CredentialRef      ID     `json:"credential_ref"`
	HostKeyFingerprint Digest `json:"host_key_fingerprint"`
}

type RepositoryImport struct {
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
	RepositoryID  ID `json:"repository_id"`
	CredentialRef ID `json:"credential_ref"`
}

type RepositoryRebase struct {
	RepositoryID ID `json:"repository_id"`
	RefID        ID `json:"ref_id"`
	OntoRefID    ID `json:"onto_ref_id"`
}

type RebaseResolve struct {
	TaskID           ID `json:"task_id"`
	ResolvedFilesRef ID `json:"resolved_files_ref"`
}

type TaskTarget struct {
	TaskID ID `json:"task_id"`
}

type ArtifactDelete struct {
	ArtifactID ID  `json:"artifact_id"`
	PlanID     *ID `json:"plan_id,omitempty"`
	TaskID     *ID `json:"task_id,omitempty"`
}

type RepositoryGCPlan struct {
	RepositoryID ID   `json:"repository_id"`
	ArtifactIDs  []ID `json:"artifact_ids,omitempty"`
}

type RepositoryGC struct {
	RepositoryID ID `json:"repository_id"`
	PlanID       ID `json:"plan_id"`
}

type RepositoryPublish struct {
	RepositoryID      ID   `json:"repository_id"`
	RefID             ID   `json:"ref_id"`
	CreatePullRequest bool `json:"create_pull_request"`
}

type PublicationMerge struct {
	PublicationID      ID     `json:"publication_id"`
	ExpectedRemoteHead Commit `json:"expected_remote_head"`
}

type ConfigurationGenerate struct {
	RepositoryID ID      `json:"repository_id"`
	Values       Values  `json:"values"`
	TemplateID   *string `json:"template_id,omitempty"`
	RefID        *ID     `json:"ref_id,omitempty"`
	BaseCommit   *Commit `json:"base_commit,omitempty"`
}

type DraftEdit struct {
	DraftID ID        `json:"draft_id"`
	Values  *Values   `json:"values,omitempty"`
	File    *FileEdit `json:"file,omitempty"`
}

type FileEdit struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ConfigurationValidate struct {
	DraftID *ID     `json:"draft_id,omitempty"`
	Source  *Source `json:"source,omitempty"`
}

type DraftTarget struct {
	DraftID ID `json:"draft_id"`
}

type OnboardingPrepare struct {
	HostID             ID                `json:"host_id"`
	ConfigurationID    ID                `json:"configuration_id"`
	ObservationID      ID                `json:"observation_id"`
	TemplateValues     map[string]Values `json:"template_values"`
	HardwareTemplateID *string           `json:"hardware_template_id,omitempty"`
	DiskTemplateID     *string           `json:"disk_template_id,omitempty"`
}

type PreparationCommit struct {
	PreparationID ID `json:"preparation_id"`
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

type MediaBuild struct {
	ProfileID string `json:"profile_id"`
	WriterID  *ID    `json:"writer_id,omitempty"`
	BuilderID *ID    `json:"builder_id,omitempty"`
}

type MediaFetch struct {
	ProfileID string  `json:"profile_id"`
	Release   *string `json:"release,omitempty"`
}

type MediaProfileRegister struct {
	Profile MediaProfile `json:"profile"`
}

type MediaPlanCreate struct {
	ImageID    ID     `json:"image_id"`
	WriterID   ID     `json:"writer_id"`
	DevicePath string `json:"device_path"`
}

type MediaWrite struct {
	PlanID          ID     `json:"plan_id"`
	TypedDevicePath string `json:"typed_device_path"`
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
