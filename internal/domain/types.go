package domain

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
)

const MaxCommandBytes = 256 * 1024
const QueueCapacity = 64
const MaxRecords = 10000
const ObservationLifetime = 35 * time.Second

var SafeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}

// StrictJSON rejects unknown fields, trailing values, and duplicate object keys.
func StrictJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func uniqueValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return errors.New("duplicate or invalid field")
				}
				seen[s] = true
				if err := uniqueValue(d); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := uniqueValue(d); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = d.Token()
	}
	return err
}

type Command struct {
	ID               string           `json:"id"`
	Type             string           `json:"type"`
	TargetID         string           `json:"target_id"`
	ExpectedVersions map[string]int64 `json:"expected_versions"`
	Payload          json.RawMessage  `json:"payload"`
}
type Receipt struct {
	CommandID  string  `json:"command_id"`
	Admission  string  `json:"admission"`
	TaskID     string  `json:"task_id"`
	ResourceID *string `json:"resource_id"`
}
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Details   any    `json:"details,omitempty"`
	Status    int    `json:"-"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func Err(code, message string, status int) *Error {
	return &Error{Code: code, Message: message, Status: status, Retryable: status == 503}
}

type Identity struct {
	SchemaVersion      int     `json:"schema_version"`
	NodeID             string  `json:"node_id"`
	KeyID              string  `json:"key_id"`
	PublicKey          string  `json:"public_key"`
	ClusterID          *string `json:"cluster_id"`
	MembershipEpoch    int     `json:"membership_epoch"`
	ClusterFingerprint *string `json:"cluster_fingerprint"`
}
type Connection struct {
	Interface *string    `json:"interface"`
	Link      string     `json:"link"`
	Address   *string    `json:"address"`
	Status    string     `json:"status"`
	CheckedAt *time.Time `json:"checked_at"`
}
type NetworkStatus struct {
	NodeID       string     `json:"node_id"`
	Environment  string     `json:"environment"`
	Network      Connection `json:"network"`
	WiFiNetworks []any      `json:"wifi_networks"`
	ConsoleURL   *string    `json:"console_url"`
	NextScreen   string     `json:"next_screen"`
	ObservedAt   *time.Time `json:"observed_at"`
	Freshness    string     `json:"freshness"`
}

func UnknownNetwork(node string) NetworkStatus {
	return NetworkStatus{NodeID: node, Environment: "installed", Network: Connection{Link: "unknown", Status: "unknown"}, WiFiNetworks: []any{}, NextScreen: "network", Freshness: "unknown"}
}

type Cancellation struct {
	Allowed   bool `json:"allowed"`
	Requested bool `json:"requested"`
}
type Task struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Version       int64           `json:"version"`
	CommandID     string          `json:"command_id"`
	Kind          string          `json:"kind"`
	State         string          `json:"state"`
	Phase         string          `json:"phase"`
	Scope         string          `json:"scope"`
	OwnerNodeID   string          `json:"owner_node_id"`
	ClusterID     *string         `json:"cluster_id"`
	CoordinatorID string          `json:"coordinator_id"`
	BuilderID     *string         `json:"builder_id"`
	TargetID      string          `json:"target_id"`
	ParentID      *string         `json:"parent_id"`
	Source        any             `json:"source"`
	Progress      any             `json:"progress"`
	Cancellation  Cancellation    `json:"cancellation"`
	Effects       map[string]bool `json:"effects"`
	Log           struct {
		NodeID     string `json:"node_id"`
		NextOffset int    `json:"next_offset"`
	} `json:"log"`
	Result    *NetworkStatus `json:"result"`
	Error     *Error         `json:"error"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func (t Task) Terminal() bool {
	return t.State == "succeeded" || t.State == "failed" || t.State == "cancelled"
}
func (t *Task) Transition(state, phase string) error {
	allowed := map[string][]string{"queued": {"running", "cancelling"}, "running": {"succeeded", "failed", "cancelling", "reconciling"}, "reconciling": {"running", "failed", "cancelling"}, "cancelling": {"cancelled"}}
	for _, next := range allowed[t.State] {
		if state == next {
			t.State = state
			t.Phase = phase
			t.Version++
			t.UpdatedAt = time.Now().UTC()
			if t.Terminal() {
				t.Cancellation.Allowed = false
			}
			return nil
		}
	}
	return errors.New("invalid task transition")
}
