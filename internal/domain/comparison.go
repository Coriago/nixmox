package domain

import "time"

type PackageOutput struct {
	Version   *string `json:"version"`
	StorePath string  `json:"store_path"`
}
type PackageChange struct {
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	Name string          `json:"name"`
	Old  []PackageOutput `json:"old"`
	New  []PackageOutput `json:"new"`
}
type DiffSummary struct {
	Added          int `json:"added"`
	Removed        int `json:"removed"`
	VersionChanged int `json:"version_changed"`
	OutputChanged  int `json:"output_changed"`
	Ambiguous      int `json:"ambiguous"`
	Unchanged      int `json:"unchanged"`
}
type ClosureSize struct {
	BeforeBytes *int64 `json:"before_bytes"`
	AfterBytes  *int64 `json:"after_bytes"`
	DeltaBytes  *int64 `json:"delta_bytes"`
}
type DiffBaseline struct {
	Kind         string    `json:"kind"`
	GenerationID string    `json:"generation_id"`
	OutputPath   string    `json:"output_path"`
	NarHash      string    `json:"nar_hash"`
	ObservedAt   time.Time `json:"observed_at"`
}
type PackageDiff struct {
	SchemaVersion    int             `json:"schema_version"`
	ID               string          `json:"id"`
	Version          int64           `json:"version"`
	HostID           string          `json:"host_id"`
	CandidateID      string          `json:"candidate_id"`
	Baseline         DiffBaseline    `json:"baseline"`
	CandidateOutput  string          `json:"candidate_output"`
	CandidateNarHash string          `json:"candidate_nar_hash"`
	Scope            string          `json:"scope"`
	State            string          `json:"state"`
	Stale            bool            `json:"stale"`
	Summary          DiffSummary     `json:"summary"`
	ClosureSize      ClosureSize     `json:"closure_size"`
	Changes          []PackageChange `json:"changes"`
	NextCursor       *string         `json:"next_cursor"`
}
type Generation struct {
	ID         string    `json:"id"`
	HostID     string    `json:"host_id"`
	Number     int       `json:"number"`
	OutputPath string    `json:"output_path"`
	NarHash    string    `json:"nar_hash"`
	Source     *Source   `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	Available  bool      `json:"available"`
	Running    bool      `json:"running"`
	Boot       bool      `json:"boot"`
}
type Deployment struct {
	ID                 string    `json:"id"`
	TaskID             string    `json:"task_id"`
	TargetID           string    `json:"target_id"`
	CandidateID        string    `json:"candidate_id"`
	DiffID             string    `json:"diff_id"`
	Mode               string    `json:"mode"`
	PreviousGeneration *int      `json:"previous_generation"`
	NewGeneration      *int      `json:"new_generation"`
	Activation         string    `json:"activation"`
	Observation        string    `json:"observation"`
	RunningGeneration  *int      `json:"running_generation"`
	BootGeneration     *int      `json:"boot_generation"`
	RunningOutput      string    `json:"running_output"`
	OutputPath         string    `json:"output_path"`
	ObservedAt         time.Time `json:"observed_at"`
}
type DeploymentApproval struct {
	AutoApprove bool   `json:"auto_approve"`
	State       string `json:"state"`
	Method      string `json:"method"`
	DiffID      string `json:"diff_id"`
	DiffVersion int64  `json:"diff_version"`
	CandidateID string `json:"candidate_id"`
	TargetID    string `json:"target_id"`
	Mode        string `json:"mode"`
	CommandID   string `json:"command_id,omitempty"`
}
