package domain

type MediaProfile struct {
	ID                    string        `json:"id"`
	Label                 string        `json:"label"`
	System                string        `json:"system"`
	Format                string        `json:"format"`
	Compression           string        `json:"compression"`
	SupportedBoards       []string      `json:"supported_boards"`
	BootMedia             []string      `json:"boot_media"`
	Source                ProfileSource `json:"source"`
	BuildSystems          []string      `json:"build_systems,omitempty"`
	EntryAdapter          *string       `json:"entry_adapter,omitempty"`
	InstalledAdapter      *string       `json:"installed_adapter,omitempty"`
	FirmwarePrerequisites []string      `json:"firmware_prerequisites,omitempty"`
	ValidationEvidence    []string      `json:"validation_evidence,omitempty"`
	ReleaseManifestURL    *string       `json:"release_manifest_url,omitempty"`
	Kexec                 *KexecPayload `json:"kexec,omitempty"`
}

type ProfileSource struct {
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
	FlakeAttribute string `json:"flake_attribute"`
	ArtifactPath   string `json:"artifact_path"`
}

type KexecPayload struct {
	KernelPath  string   `json:"kernel_path"`
	InitrdPath  string   `json:"initrd_path"`
	CommandLine []string `json:"command_line"`
	SHA256      Digest   `json:"sha256"`
}

type Media struct {
	Record
	OwnerNodeID       ID               `json:"owner_node_id"`
	ProfileID         string           `json:"profile_id"`
	TargetSystem      string           `json:"target_system"`
	Format            string           `json:"format"`
	Compression       string           `json:"compression"`
	SupportedBoards   []string         `json:"supported_boards"`
	BootMedia         []string         `json:"boot_media"`
	BuilderID         *ID              `json:"builder_id"`
	RecipeCommit      *Commit          `json:"recipe_commit"`
	DownloadSizeBytes uint64           `json:"download_size_bytes"`
	ExpandedSizeBytes uint64           `json:"expanded_size_bytes"`
	ImageDigest       Digest           `json:"image_digest"`
	ExpandedDigest    Digest           `json:"expanded_digest"`
	DownloadURL       string           `json:"download_url"`
	ReleaseManifest   *ReleaseManifest `json:"release_manifest"`
}

type ReleaseManifest struct {
	ID             string `json:"id"`
	ProfileID      string `json:"profile_id"`
	URL            string `json:"url"`
	SHA256         Digest `json:"sha256"`
	ExpandedSHA256 Digest `json:"expanded_sha256"`
	Bytes          uint64 `json:"bytes"`
	ExpandedBytes  uint64 `json:"expanded_bytes"`
}

type MediaPlan struct {
	Record
	ImageID         ID     `json:"image_id"`
	WriterID        ID     `json:"writer_id"`
	ImageDigest     Digest `json:"image_digest"`
	DeviceID        ID     `json:"device_id"`
	DevicePath      string `json:"device_path"`
	DeviceSizeBytes uint64 `json:"device_size_bytes"`
	ObservationID   ID     `json:"observation_id"`
	Review          Review `json:"review"`
}
