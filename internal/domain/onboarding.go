package domain

import "time"

type Observation struct {
	SchemaVersion  int            `json:"schema_version"`
	ID             ID             `json:"id"`
	HostID         ID             `json:"host_id"`
	ObservedAt     time.Time      `json:"observed_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
	System         string         `json:"system"`
	BootMode       string         `json:"boot_mode"`
	HardwareDigest Digest         `json:"hardware_digest"`
	Disks          []Disk         `json:"disks"`
	Partitions     []Partition    `json:"partitions"`
	PublicIdentity PublicIdentity `json:"public_identity"`
	Network        NetworkHandoff `json:"network"`
}

type Disk struct {
	ID                ID       `json:"id"`
	Path              string   `json:"path"`
	Model             string   `json:"model"`
	Serial            string   `json:"serial"`
	SizeBytes         uint64   `json:"size_bytes"`
	Eligible          bool     `json:"eligible"`
	IneligibleReasons []string `json:"ineligible_reasons"`
}

type Partition struct {
	ID                ID       `json:"id"`
	Path              string   `json:"path"`
	FsType            string   `json:"fs_type"`
	ParentDiskID      ID       `json:"parent_disk_id"`
	Eligible          bool     `json:"eligible"`
	IneligibleReasons []string `json:"ineligible_reasons"`
}

type PublicIdentity struct {
	NodeID       ID      `json:"node_id"`
	AgeRecipient *string `json:"age_recipient"`
}

type NetworkHandoff struct {
	ActiveInterfaces []string `json:"active_interfaces"`
	HandoffReady     bool     `json:"handoff_ready"`
}

type HardwareFacts struct {
	InitrdAvailableKernelModules *[]string `json:"initrd_available_kernel_modules"`
	KernelModules                *[]string `json:"kernel_modules"`
}

type Preparation struct {
	Record
	HostID           ID                   `json:"host_id"`
	ConfigurationID  ID                   `json:"configuration_id"`
	ObservationID    ID                   `json:"observation_id"`
	TemplateValues   map[string]Values    `json:"template_values"`
	Templates        PreparationSelection `json:"templates"`
	SOPS             SOPSPreparation      `json:"sops"`
	State            string               `json:"state"`
	DraftID          ID                   `json:"draft_id"`
	SourceCommit     *Commit              `json:"source_commit"`
	Confirmation     *Review              `json:"confirmation"`
	ExpectedVersions Versions             `json:"expected_versions"`
	GeneratedOutputs []GeneratedOutput    `json:"generated_outputs"`
	Assets           []PinnedAsset        `json:"assets"`
}

type PreparationSelection struct {
	Hardware         string `json:"hardware"`
	Disk             string `json:"disk"`
	RepositoryCommit Commit `json:"repository_commit"`
}

type PinnedAsset struct {
	Path         string `json:"path"`
	SourceCommit Commit `json:"source_commit"`
	SHA256       Digest `json:"sha256"`
}

type SOPSPreparation struct {
	Updater       string   `json:"updater"`
	CredentialRef *ID      `json:"credential_ref"`
	UpdateRules   []string `json:"update_rules"`
	State         string   `json:"state"`
	Reason        *string  `json:"reason"`
}

type Handoff struct {
	Record
	NodeID              ID             `json:"node_id"`
	KeyID               ID             `json:"key_id"`
	TaskID              ID             `json:"task_id"`
	BootSessionID       ID             `json:"boot_session_id"`
	ExpectedEnvironment Environment    `json:"expected_environment"`
	State               string         `json:"state"`
	ProtectedDeviceIDs  []ID           `json:"protected_device_ids"`
	Manifest            []FileManifest `json:"manifest"`
	Network             NetworkHandoff `json:"network"`
}

type InstallerStatus struct {
	HostID                ID              `json:"host_id"`
	InstallerID           *ID             `json:"installer_id"`
	ImageID               *ID             `json:"image_id"`
	Methods               map[string]bool `json:"methods"`
	InstallationSupported bool            `json:"installation_supported"`
	UnsupportedReasons    []string        `json:"unsupported_reasons"`
	BootEntry             *string         `json:"boot_entry"`
	EntryAdapter          *string         `json:"entry_adapter"`
	InstalledAdapter      *string         `json:"installed_adapter"`
	State                 string          `json:"state"`
	ProtectedDeviceIDs    []ID            `json:"protected_device_ids"`
	PreserveIdentity      bool            `json:"preserve_identity"`
	NetworkHandoff        string          `json:"network_handoff"`
}

type BootSelection struct {
	TaskID    ID      `json:"task_id"`
	Adapter   string  `json:"adapter"`
	Prior     string  `json:"prior"`
	Desired   string  `json:"desired"`
	Observed  *string `json:"observed"`
	HandoffID ID      `json:"handoff_id"`
}
