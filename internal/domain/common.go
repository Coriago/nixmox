// Package domain defines transport-independent, versioned Nixmox contracts.
package domain

import (
	"encoding/json"
	"time"
)

type ID string
type Version uint64
type Digest string
type Commit string
type Scope string

const (
	NodeLocal     Scope = "node_local"
	ClusterScope  Scope = "cluster"
	Inherited     Scope = "inherited"
	SchemaVersion       = 1
)

type Record struct {
	SchemaVersion int     `json:"schema_version"`
	ID            ID      `json:"id"`
	Version       Version `json:"version"`
}
type Freshness string

const (
	Live    Freshness = "live"
	Stale   Freshness = "stale"
	Unknown Freshness = "unknown"
)

type Observed struct {
	ObservedAt *time.Time `json:"observed_at"`
	Freshness  Freshness  `json:"freshness"`
}
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
type PageRequest struct {
	Cursor  string
	Limit   int
	Filters map[string]string
}
type Versions map[string]Version
type TraceContext map[string]string
type Values map[string]any
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func Unavailable(component string) error {
	return &Error{Code: "UNSUPPORTED_CAPABILITY", Message: component + " is not implemented"}
}

type ErrorResponse struct {
	Error     *Error `json:"error"`
	CommandID *ID    `json:"command_id"`
}
type Command struct {
	ID               ID              `json:"id"`
	Type             string          `json:"type"`
	TargetID         ID              `json:"target_id"`
	ExpectedVersions Versions        `json:"expected_versions"`
	Payload          json.RawMessage `json:"payload"`
}

// Envelope is service-created; callers cannot select scope, expiry or authority.
type Envelope struct {
	MembershipEpoch uint64          `json:"membership_epoch,omitempty"`
	SchemaVersion   int             `json:"schema_version"`
	Command         Command         `json:"command"`
	Scope           Scope           `json:"scope"`
	OwnerNodeID     ID              `json:"owner_node_id"`
	ClusterID       *ID             `json:"cluster_id"`
	AdmittedAt      time.Time       `json:"admitted_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
	RequestHash     Digest          `json:"request_hash"`
	Trace           TraceContext    `json:"trace"`
	Resolved        json.RawMessage `json:"resolved"`
}
type Receipt struct {
	CommandID  ID     `json:"command_id"`
	Admission  string `json:"admission"`
	TaskID     *ID    `json:"task_id"`
	ResourceID *ID    `json:"resource_id"`
}
type Acknowledgements struct {
	ConfirmHost      *string  `json:"confirm_host,omitempty"`
	TypedDevicePaths []string `json:"typed_device_paths,omitempty"`
}
type Review struct {
	ID                       ID                `json:"id"`
	Kind                     string            `json:"kind"`
	State                    string            `json:"state"`
	Binding                  json.RawMessage   `json:"binding"`
	RequiredAcknowledgements Acknowledgements  `json:"required_acknowledgements"`
	Confirmation             *Acknowledgements `json:"confirmation"`
}

// Event is a stream envelope, not a JSON resource. SSE writes Name as the event
// name and Payload as its data without wrapping the documented resource fields.
type Event struct {
	Name    string
	Payload json.RawMessage
}
type Download struct {
	OwnerNodeID           ID
	Filename, ContentType string
	Bytes                 int64
	SHA256                Digest
	ExpiresAt             *time.Time
}
