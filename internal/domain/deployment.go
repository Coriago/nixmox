package domain

import "time"

type Candidate struct {
	Record
	Source          Source      `json:"source"`
	BuilderID       ID          `json:"builder_id"`
	System          string      `json:"system"`
	State           string      `json:"state"`
	OutputPath      *string     `json:"output_path"`
	NARHash         *Digest     `json:"nar_hash"`
	InputLockDigest Digest      `json:"input_lock_digest"`
	Comparison      *Comparison `json:"comparison"`
}

type Comparison struct {
	DiffID     ID     `json:"diff_id"`
	HostID     ID     `json:"host_id"`
	Generation uint64 `json:"generation"`
	Stale      bool   `json:"stale"`
}

type Deployment struct {
	Record
	TaskID             ID             `json:"task_id"`
	TargetID           ID             `json:"target_id"`
	CandidateID        ID             `json:"candidate_id"`
	Mode               ActivationMode `json:"mode"`
	PreviousGeneration *uint64        `json:"previous_generation"`
	NewGeneration      *uint64        `json:"new_generation"`
	Activation         string         `json:"activation"`
	Observation        string         `json:"observation"`
	RunningGeneration  *uint64        `json:"running_generation"`
	BootGeneration     *uint64        `json:"boot_generation"`
	CreatedAt          time.Time      `json:"created_at"`
}

type Generation struct {
	ID         ID        `json:"id"`
	HostID     ID        `json:"host_id"`
	Number     uint64    `json:"number"`
	CreatedAt  time.Time `json:"created_at"`
	Source     Source    `json:"source"`
	OutputPath string    `json:"output_path"`
	NARHash    Digest    `json:"nar_hash"`
	Available  bool      `json:"available"`
	Running    bool      `json:"running"`
	Boot       bool      `json:"boot"`
	Pins       []ID      `json:"pins"`
}

type PackageDiff struct {
	Record
	HostID          ID              `json:"host_id"`
	CandidateID     ID              `json:"candidate_id"`
	Baseline        Baseline        `json:"baseline"`
	CandidateOutput string          `json:"candidate_output"`
	Scope           string          `json:"scope"`
	State           string          `json:"state"`
	Stale           bool            `json:"stale"`
	Summary         DiffSummary     `json:"summary"`
	ClosureSize     ClosureSize     `json:"closure_size"`
	Changes         []PackageChange `json:"changes"`
	NextCursor      *string         `json:"next_cursor"`
}

type Baseline struct {
	Kind         string     `json:"kind"`
	GenerationID *ID        `json:"generation_id"`
	OutputPath   string     `json:"output_path"`
	ObservedAt   *time.Time `json:"observed_at"`
}

type DiffSummary struct {
	Added          uint64 `json:"added"`
	Removed        uint64 `json:"removed"`
	VersionChanged uint64 `json:"version_changed"`
	OutputChanged  uint64 `json:"output_changed"`
	Ambiguous      uint64 `json:"ambiguous"`
	Unchanged      uint64 `json:"unchanged"`
}

type ClosureSize struct {
	BeforeBytes *uint64 `json:"before_bytes"`
	AfterBytes  *uint64 `json:"after_bytes"`
	DeltaBytes  *int64  `json:"delta_bytes"`
}

type PackageChange struct {
	ID   ID              `json:"id"`
	Kind string          `json:"kind"`
	Name string          `json:"name"`
	Old  []PackageOutput `json:"old"`
	New  []PackageOutput `json:"new"`
}

type PackageOutput struct {
	Version   *string `json:"version"`
	StorePath string  `json:"store_path"`
}

type ClosurePath struct {
	StorePath     string   `json:"store_path"`
	Name          *string  `json:"name"`
	Version       *string  `json:"version"`
	NARHash       Digest   `json:"nar_hash"`
	NARSize       *uint64  `json:"nar_size"`
	References    []string `json:"references"`
	SystemPackage *bool    `json:"system_package"`
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
