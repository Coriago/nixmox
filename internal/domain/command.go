package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// PreparedCommand is internal transport data, never a console request schema.
type PreparedCommand struct {
	Command     Command         `json:"command"`
	ClusterID   string          `json:"cluster_id"`
	Fingerprint string          `json:"fingerprint"`
	Kind        string          `json:"kind"`
	Resolved    json.RawMessage `json:"resolved"`
	Exclusions  []string        `json:"exclusions,omitempty"`
	AdmittedAt  time.Time       `json:"admitted_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

// CanonicalCommand recursively sorts object keys without rounding JSON numbers.
// Both local receipts and the durable cluster ingress use this exact identity.
func CanonicalCommand(c Command) (Command, string, error) {
	if !SafeID.MatchString(c.ID) || !SafeID.MatchString(c.TargetID) || c.Type == "" {
		return c, "", Err("INVALID_COMMAND", "id, type and target_id are required.", 422)
	}
	var object map[string]json.RawMessage
	if len(c.Payload) > MaxCommandBytes || StrictJSON(c.Payload, &object) != nil || object == nil {
		return c, "", Err("INVALID_PAYLOAD", "A bounded object payload is required.", 422)
	}
	if raw, ok := object["host_id"]; ok {
		var host string
		if json.Unmarshal(raw, &host) != nil || host != c.TargetID {
			return c, "", Err("INVALID_PAYLOAD", "Payload host_id must equal target_id.", 422)
		}
	}
	for key, v := range c.ExpectedVersions {
		if key == "" || v <= 0 {
			return c, "", Err("INVALID_COMMAND", "Expected versions must have resource keys and positive values.", 422)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return c, "", Err("INVALID_PAYLOAD", "Invalid object payload.", 422)
	}
	c.Payload, _ = json.Marshal(value)
	if c.ExpectedVersions == nil {
		c.ExpectedVersions = map[string]int64{}
	}
	b, err := json.Marshal(c)
	if err != nil || len(b) > MaxCommandBytes {
		return c, "", Err("INVALID_PAYLOAD", "Command exceeds 256 KiB.", 422)
	}
	h := sha256.Sum256(b)
	return c, hex.EncodeToString(h[:]), nil
}
