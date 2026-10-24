package domain

import (
	"encoding/json"
	"time"
)

type Task struct {
	MembershipEpoch uint64 `json:"membership_epoch,omitempty"`
	Record
	CommandID     ID              `json:"command_id"`
	Kind          string          `json:"kind"`
	Scope         Scope           `json:"scope"`
	OwnerNodeID   ID              `json:"owner_node_id"`
	ClusterID     *ID             `json:"cluster_id"`
	State         TaskState       `json:"state"`
	Phase         string          `json:"phase"`
	CoordinatorID ID              `json:"coordinator_id"`
	BuilderID     *ID             `json:"builder_id"`
	TargetID      ID              `json:"target_id"`
	ParentID      *ID             `json:"parent_id"`
	RetryOf       *ID             `json:"retry_of"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Source        *Source         `json:"source"`
	Progress      Counter         `json:"progress"`
	Cancellation  Cancellation    `json:"cancellation"`
	Effects       map[string]bool `json:"effects"`
	Log           LogReference    `json:"log"`
	Approval      *Approval       `json:"approval"`
	Review        *Review         `json:"review"`
	Result        json.RawMessage `json:"result"`
	Error         *Error          `json:"error"`
}

type Cancellation struct {
	Allowed   bool    `json:"allowed"`
	Requested bool    `json:"requested"`
	Reason    *string `json:"reason"`
}

type Counter struct {
	Completed *uint64 `json:"completed"`
	Total     *uint64 `json:"total"`
	Unit      string  `json:"unit"`
	ByteBasis *string `json:"byte_basis,omitempty"`
}

type LogReference struct {
	NodeID     ID    `json:"node_id"`
	NextOffset int64 `json:"next_offset"`
}

type LogChunk struct {
	Offset       int64  `json:"offset"`
	DataBase64   string `json:"data_base64"`
	NextOffset   int64  `json:"next_offset"`
	RetainedFrom int64  `json:"retained_from"`
	Complete     bool   `json:"complete"`
	Truncated    bool   `json:"truncated"`
}

type LogEvent struct {
	TaskID      ID     `json:"task_id"`
	ActivityID  *ID    `json:"activity_id,omitempty"`
	OwnerNodeID ID     `json:"owner_node_id"`
	Offset      int64  `json:"offset"`
	DataBase64  string `json:"data_base64"`
}

type Approval struct {
	AutoApprove bool           `json:"auto_approve"`
	State       string         `json:"state"`
	Method      *string        `json:"method"`
	DiffID      ID             `json:"diff_id"`
	DiffVersion Version        `json:"diff_version"`
	CandidateID ID             `json:"candidate_id"`
	TargetID    ID             `json:"target_id"`
	Mode        ActivationMode `json:"mode"`
}

type TaskProgress struct {
	Unavailable   []MeasurementError `json:"unavailable,omitempty"`
	SchemaVersion int                `json:"schema_version"`
	TaskID        ID                 `json:"task_id"`
	Version       Version            `json:"version"`
	Observed
	Phase             string           `json:"phase"`
	PlanComplete      bool             `json:"plan_complete"`
	Downloads         DownloadProgress `json:"downloads"`
	Builds            BuildProgress    `json:"builds"`
	ActiveActivityIDs []ID             `json:"active_activity_ids"`
}

type DownloadProgress struct {
	CompletedPaths *uint64  `json:"completed_paths"`
	TotalPaths     *uint64  `json:"total_paths"`
	ActivePaths    *uint64  `json:"active_paths"`
	BytesReceived  *uint64  `json:"bytes_received"`
	BytesTotal     *uint64  `json:"bytes_total"`
	ByteBasis      string   `json:"byte_basis"`
	BytesPerSecond *float64 `json:"bytes_per_second"`
	ETASeconds     *float64 `json:"eta_seconds"`
	TotalsKind     string   `json:"totals_kind"`
}

type BuildProgress struct {
	CompletedDerivations *uint64 `json:"completed_derivations"`
	TotalDerivations     *uint64 `json:"total_derivations"`
	ActiveDerivations    *uint64 `json:"active_derivations"`
	FailedDerivations    *uint64 `json:"failed_derivations"`
}

type Activity struct {
	ID             ID              `json:"id"`
	TaskID         ID              `json:"task_id"`
	Version        Version         `json:"version"`
	Attempt        uint64          `json:"attempt"`
	Kind           string          `json:"kind"`
	State          string          `json:"state"`
	NodeID         ID              `json:"node_id"`
	Name           string          `json:"name"`
	StorePath      *string         `json:"store_path"`
	DerivationPath *string         `json:"derivation_path"`
	Source         *ActivitySource `json:"source"`
	Progress       Counter         `json:"progress"`
	BytesPerSecond *float64        `json:"bytes_per_second"`
	ElapsedSeconds float64         `json:"elapsed_seconds"`
	BuildPhase     *string         `json:"build_phase"`
	LogRef         *string         `json:"log_ref"`
}

type ActivitySource struct {
	Kind string  `json:"kind"`
	URL  *string `json:"url"`
}

type ProgressEvent struct {
	TaskID  ID      `json:"task_id"`
	Version Version `json:"version"`
	Observed
	Progress TaskProgress `json:"progress"`
}
