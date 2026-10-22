package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
	"nixmox/internal/pki"
)

type joinRecord struct {
	SchemaVersion     int                      `json:"schema_version"`
	View              domain.JoinRequest       `json:"view"`
	Request           domain.EnrollmentRequest `json:"request"`
	Owner             string                   `json:"owner"`
	Binding           string                   `json:"binding"`
	DecisionCommandID string                   `json:"decision_command_id,omitempty"`
	Issued            *domain.Issuance         `json:"issued,omitempty"`
}
type Member struct {
	SchemaVersion int    `json:"schema_version"`
	Version       int64  `json:"version"`
	NodeID        string `json:"node_id"`
	State         string `json:"state"`
	Epoch         int    `json:"membership_epoch"`
	DaemonKey     string `json:"daemon_key"`
	BrokerKey     string `json:"broker_key"`
	RequestID     string `json:"request_id"`
	BrokerRole    string `json:"broker_role"`
}
type joinRate struct {
	Start time.Time
	Count int
}

func (m *Manager) limitJoin(owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.joinRates == nil {
		m.joinRates = map[string]joinRate{}
	}
	now := time.Now()
	for key, rate := range m.joinRates {
		if now.Sub(rate.Start) >= time.Minute {
			delete(m.joinRates, key)
		}
	}
	rate, ok := m.joinRates[owner]
	if !ok {
		if len(m.joinRates) >= 256 {
			return domain.Err("RATE_LIMITED", "Enrollment capacity is occupied.", 429)
		}
		rate = joinRate{Start: now}
	}
	if rate.Count >= 3 {
		return domain.Err("RATE_LIMITED", "New enrollment attempts are limited to three per minute.", 429)
	}
	rate.Count++
	m.joinRates[owner] = rate
	return nil
}
func (m *Manager) enrollmentState() (nats.KeyValue, Membership, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.membership == nil || m.stores == nil || m.identity.ClusterID == nil {
		return nil, Membership{}, unavailable()
	}
	return m.stores.State, *m.membership, nil
}
func readJoin(kv nats.KeyValue, id string) (joinRecord, uint64, error) {
	var record joinRecord
	entry, err := kv.Get("join." + id)
	if err != nil {
		return record, 0, err
	}
	if domain.StrictJSON(entry.Value(), &record) != nil || record.SchemaVersion != 1 || record.View.ID != id {
		return record, 0, domain.Err("UNAVAILABLE", "Enrollment record unavailable.", 503)
	}
	return record, entry.Revision(), nil
}
func publicJoin(record joinRecord) domain.JoinRequest {
	v := record.View
	if v.State == "awaiting_approval" && time.Now().After(v.ExpiresAt) {
		v.State = "expired"
	}
	return v
}
func (m *Manager) SubmitEnrollment(owner string, request domain.EnrollmentRequest) (any, error) {
	if len(owner) != 64 || !domain.SafeID.MatchString(request.ID) || !domain.SafeID.MatchString(request.NodeID) || len(request.Name) < 1 || len(request.Name) > 128 || strings.ContainsAny(request.Name, "\x00\r\n") || !clusterName.MatchString(request.ExpectedClusterName) {
		return nil, domain.Err("INVALID_PAYLOAD", "Invalid enrollment identity or cluster name.", 422)
	}
	daemon, err := pki.ParseCSR([]byte(request.DaemonCSR))
	if err != nil || pki.Fingerprint(daemon.PublicKey) != owner {
		return nil, domain.Err("UNAUTHORIZED", "Daemon CSR must prove the TLS owner's key.", 401)
	}
	broker, err := pki.ParseCSR([]byte(request.BrokerCSR))
	if err != nil || pki.Fingerprint(broker.PublicKey) == owner {
		return nil, domain.Err("INVALID_PAYLOAD", "A distinct signed broker CSR is required.", 422)
	}
	kv, membership, err := m.enrollmentState()
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(struct {
		Owner   string
		Request domain.EnrollmentRequest
	}{owner, request})
	sum := sha256.Sum256(b)
	binding := hex.EncodeToString(sum[:])
	record, _, err := readJoin(kv, request.ID)
	if err == nil {
		if record.Binding != binding {
			return nil, domain.Err("IDEMPOTENCY_CONFLICT", "Enrollment ID is already bound to another request.", 409)
		}
		return publicJoin(record), nil
	}
	if !errors.Is(err, nats.ErrKeyNotFound) {
		return nil, unavailable()
	}
	if request.ExpectedClusterName != membership.Name {
		return nil, domain.Err("CLUSTER_NAME_MISMATCH", "The peer belongs to a different cluster name.", 409)
	}
	if request.NodeID == membership.IssuerNodeID {
		return nil, domain.Err("IDENTITY_CONFLICT", "This node ID is already a cluster identity.", 409)
	}
	if entry, err := kv.Get("member." + request.NodeID); err == nil {
		var member Member
		if domain.StrictJSON(entry.Value(), &member) != nil {
			return nil, unavailable()
		}
		if member.DaemonKey != owner || member.BrokerKey != pki.Fingerprint(broker.PublicKey) {
			return nil, domain.Err("IDENTITY_CONFLICT", "Node ID is bound to different keys.", 409)
		}
		if member.State == "pending" || member.State == "full" {
			return nil, domain.Err("INVALID_STATE", "This node already has an active membership.", 409)
		}
	} else if !errors.Is(err, nats.ErrKeyNotFound) {
		return nil, unavailable()
	}
	if err = m.limitJoin(owner); err != nil {
		return nil, err
	}
	status, err := kv.Status()
	if err != nil {
		return nil, unavailable()
	}
	if status.Values() >= domain.MaxRecords {
		return nil, domain.Err("QUEUE_FULL", "Enrollment storage capacity is exhausted.", 503)
	}
	block, _ := pem.Decode([]byte(membership.CA))
	ca, err := parseCA(block)
	if err != nil {
		return nil, unavailable()
	}
	record = joinRecord{SchemaVersion: 1, Request: request, Owner: owner, Binding: binding, View: domain.JoinRequest{ID: request.ID, Version: 1, NodeID: request.NodeID, Name: request.Name, KeyFingerprint: owner, State: "awaiting_approval", ExpiresAt: time.Now().UTC().Add(10 * time.Minute), ClusterID: membership.ClusterID, ClusterFingerprint: pki.Fingerprint(ca.PublicKey)}}
	b, _ = json.Marshal(record)
	if _, err = kv.Create("join."+request.ID, b); err != nil {
		existing, _, readErr := readJoin(kv, request.ID)
		if readErr != nil {
			return nil, domain.Err("OUTCOME_UNKNOWN", "Enrollment durability is uncertain; retry this request ID.", 503)
		}
		if existing.Binding != binding {
			return nil, domain.Err("IDEMPOTENCY_CONFLICT", "Enrollment ID is already bound to another request.", 409)
		}
		return publicJoin(existing), nil
	}
	m.changed()
	return record.View, nil
}
func (m *Manager) Read(owner, id string) (any, error) {
	kv, _, err := m.enrollmentState()
	if err != nil {
		return nil, err
	}
	record, _, err := readJoin(kv, id)
	if errors.Is(err, nats.ErrKeyNotFound) || err == nil && record.Owner != owner {
		return nil, domain.Err("NOT_FOUND", "Enrollment request not found.", 404)
	}
	if err != nil {
		return nil, unavailable()
	}
	return publicJoin(record), nil
}
func (m *Manager) Certificates(owner, id string) (any, error) {
	kv, _, err := m.enrollmentState()
	if err != nil {
		return nil, err
	}
	record, _, err := readJoin(kv, id)
	if errors.Is(err, nats.ErrKeyNotFound) || err == nil && record.Owner != owner {
		return nil, domain.Err("NOT_FOUND", "Enrollment request not found.", 404)
	}
	if err != nil {
		return nil, unavailable()
	}
	if record.View.State != "approved" || record.Issued == nil {
		return nil, domain.Err("APPROVAL_REQUIRED", "This enrollment has no approved issuance.", 409)
	}
	entry, err := kv.Get("member." + record.View.NodeID)
	if err != nil {
		return nil, unavailable()
	}
	var member Member
	if domain.StrictJSON(entry.Value(), &member) != nil {
		return nil, unavailable()
	}
	if member.State == "enrolling" {
		return nil, domain.Err("OUTCOME_UNKNOWN", "Approved membership grants are being reconciled.", 503)
	}
	if member.Epoch != record.Issued.Epoch || member.DaemonKey != owner || (member.State != "pending" && member.State != "full") {
		return nil, domain.Err("MEMBERSHIP_REVOKED", "This issuance no longer has an active membership.", 403)
	}
	return record.Issued, nil
}
