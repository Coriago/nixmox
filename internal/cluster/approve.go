package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/broker"
	"nixmox/internal/domain"
	"nixmox/internal/network"
	"nixmox/internal/pki"
	"nixmox/internal/service"
)

type decisionPayload struct {
	RequestID string `json:"request_id"`
}
type pinnedDecision struct {
	RequestID string `json:"request_id"`
	Version   int64  `json:"version"`
	Binding   string `json:"binding"`
}

func (m *Manager) enrollmentOperations() []service.Operation {
	var operations []service.Operation
	for _, kind := range []string{"cluster.request.approve", "cluster.request.reject"} {
		run := func(ctx context.Context, e *service.Execution) (any, error) {
			var pinned pinnedDecision
			if domain.StrictJSON(e.Resolved, &pinned) != nil {
				return nil, domain.Err("INVALID_STATE", "Approval binding unavailable.", 409)
			}
			if err := e.Checkpoint("decision", pinned, false); err != nil {
				return nil, err
			}
			return m.decide(ctx, e.Command, pinned)
		}
		operations = append(operations, service.Operation{Kind: kind, Scope: "cluster", Timeout: 30 * time.Second, SafeRetry: true, Validate: func(c domain.Command) error {
			var p decisionPayload
			if domain.StrictJSON(c.Payload, &p) != nil || !domain.SafeID.MatchString(p.RequestID) || len(c.ExpectedVersions) != 1 || c.ExpectedVersions["join_request:"+p.RequestID] < 1 {
				return domain.Err("INVALID_PAYLOAD", "A request_id and its reviewed version are required.", 422)
			}
			return nil
		}, Resolve: func(ctx context.Context, c domain.Command) (json.RawMessage, error) {
			kv, member, err := m.enrollmentState()
			if err != nil {
				return nil, err
			}
			if c.TargetID != member.IssuerNodeID {
				return nil, domain.Err("TARGET_UNREACHABLE", "Enrollment decisions target the cluster issuer.", 503)
			}
			var p decisionPayload
			json.Unmarshal(c.Payload, &p)
			record, _, err := readJoin(kv, p.RequestID)
			if errors.Is(err, nats.ErrKeyNotFound) {
				return nil, domain.Err("NOT_FOUND", "Join request not found.", 404)
			}
			if err != nil {
				return nil, unavailable()
			}
			if record.View.Version != c.ExpectedVersions["join_request:"+p.RequestID] {
				return nil, domain.Err("VERSION_CONFLICT", "Join request changed after review.", 409)
			}
			if publicJoin(record).State != "awaiting_approval" {
				return nil, domain.Err("INVALID_STATE", "Join request is no longer awaiting approval.", 409)
			}
			return json.Marshal(pinnedDecision{RequestID: p.RequestID, Version: record.View.Version, Binding: record.Binding})
		}, Exclusions: func(c domain.Command, resolved json.RawMessage) ([]string, error) {
			var p pinnedDecision
			if err := domain.StrictJSON(resolved, &p); err != nil {
				return nil, err
			}
			return []string{"join:" + p.RequestID}, nil
		}, Run: run, Reconcile: run})
	}
	return operations
}
func (m *Manager) decide(ctx context.Context, command domain.Command, pinned pinnedDecision) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	kv, local, err := m.enrollmentState()
	if err != nil {
		return nil, err
	}
	record, revision, err := readJoin(kv, pinned.RequestID)
	if err != nil {
		return nil, unavailable()
	}
	if record.Binding != pinned.Binding {
		return nil, domain.Err("IDENTITY_CONFLICT", "Enrollment binding changed.", 409)
	}
	already := record.DecisionCommandID == command.ID
	if !already && (record.View.Version != pinned.Version || publicJoin(record).State != "awaiting_approval") {
		return nil, domain.Err("VERSION_CONFLICT", "Enrollment review is stale or expired.", 409)
	}
	if command.Type == "cluster.request.reject" {
		if already && record.View.State == "rejected" {
			return publicJoin(record), nil
		}
		decision := "rejected"
		record.View.State = decision
		record.View.Decision = &decision
		record.View.Version++
		record.DecisionCommandID = command.ID
		b, _ := json.Marshal(record)
		if _, err = kv.Update("join."+record.View.ID, b, revision); err != nil {
			return nil, unavailable()
		}
		return publicJoin(record), nil
	}
	member, memberRevision, err := reserveMember(ctx, kv, record)
	if err != nil {
		return nil, err
	}
	if record.Issued == nil {
		a, err := pki.OpenAuthority(m.store, local.ClusterID, false)
		if err != nil {
			return nil, domain.Err("CREDENTIAL_UNAVAILABLE", "The cluster issuer is unavailable.", 503)
		}
		daemonCSR, err := pki.ParseCSR([]byte(record.Request.DaemonCSR))
		if err != nil {
			return nil, domain.Err("INVALID_STATE", "Stored daemon CSR is invalid.", 409)
		}
		brokerCSR, err := pki.ParseCSR([]byte(record.Request.BrokerCSR))
		if err != nil {
			return nil, domain.Err("INVALID_STATE", "Stored broker CSR is invalid.", 409)
		}
		daemonCert, err := a.Issue(daemonCSR, member.NodeID, "daemon", member.Epoch)
		if err != nil {
			return nil, err
		}
		brokerCert, err := a.Issue(brokerCSR, member.NodeID, "broker", member.Epoch)
		if err != nil {
			return nil, err
		}
		endpoints, err := m.leafEndpoints()
		if err != nil {
			return nil, err
		}
		if time.Now().After(record.View.ExpiresAt) {
			return nil, domain.Err("REQUEST_EXPIRED", "The enrollment request expired before issuance was committed.", 409)
		}
		record.Issued = &domain.Issuance{RequestID: record.View.ID, ClusterID: local.ClusterID, ClusterName: local.Name, NodeID: member.NodeID, Epoch: member.Epoch, CA: string(a.PEM), DaemonCertificate: string(daemonCert), BrokerCertificate: string(brokerCert), IssuerNodeID: local.IssuerNodeID, IssuerEpoch: local.Epoch, IssuerBrokerCertificate: local.BrokerCertificate, Endpoints: endpoints}
		decision := "approved"
		record.View.State = decision
		record.View.Decision = &decision
		record.View.Version++
		record.DecisionCommandID = command.ID
		b, _ := json.Marshal(record)
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if _, err = kv.Update("join."+record.View.ID, b, revision); err != nil {
			return nil, unavailable()
		}
	}
	m.mu.RLock()
	b := m.broker
	m.mu.RUnlock()
	if b == nil {
		return nil, unavailable()
	}
	if err = loadLeafGrants(ctx, kv, b); err != nil {
		return nil, err
	}
	if member.State == "enrolling" {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		member.State = "pending"
		member.Version++
		data, _ := json.Marshal(member)
		if _, err = kv.Update("member."+member.NodeID, data, memberRevision); err != nil {
			return nil, unavailable()
		}
	}
	return publicJoin(record), nil
}
func reserveMember(ctx context.Context, kv nats.KeyValue, record joinRecord) (Member, uint64, error) {
	brokerCSR, err := pki.ParseCSR([]byte(record.Request.BrokerCSR))
	if err != nil {
		return Member{}, 0, domain.Err("INVALID_STATE", "Stored broker CSR is invalid.", 409)
	}
	member := Member{SchemaVersion: 1, Version: 1, NodeID: record.View.NodeID, State: "enrolling", Epoch: 1, DaemonKey: record.Owner, BrokerKey: pki.Fingerprint(brokerCSR.PublicKey), RequestID: record.View.ID, BrokerRole: "leaf"}
	for role, key := range map[string]string{"daemon": member.DaemonKey, "broker": member.BrokerKey} {
		if err := ctx.Err(); err != nil {
			return member, 0, err
		}
		claim := struct {
			NodeID string `json:"node_id"`
			Role   string `json:"role"`
		}{member.NodeID, role}
		data, _ := json.Marshal(claim)
		if _, err := kv.Create("key."+key, data); err != nil {
			entry, getErr := kv.Get("key." + key)
			if getErr != nil {
				return member, 0, unavailable()
			}
			if string(entry.Value()) != string(data) {
				return member, 0, domain.Err("IDENTITY_CONFLICT", "A machine key is already bound to another identity.", 409)
			}
		}
	}
	data, _ := json.Marshal(member)
	revision, err := kv.Create("member."+member.NodeID, data)
	if err == nil {
		return member, revision, nil
	}
	entry, err := kv.Get("member." + member.NodeID)
	if err != nil {
		return member, 0, unavailable()
	}
	var existing Member
	if domain.StrictJSON(entry.Value(), &existing) != nil {
		return member, 0, unavailable()
	}
	if existing.DaemonKey != member.DaemonKey || existing.BrokerKey != member.BrokerKey {
		return member, 0, domain.Err("IDENTITY_CONFLICT", "Node identity is bound to different keys.", 409)
	}
	if existing.RequestID == member.RequestID {
		return existing, entry.Revision(), nil
	}
	if existing.State == "enrolling" {
		old, _, err := readJoin(kv, existing.RequestID)
		if err != nil || publicJoin(old).State != "expired" || old.Issued != nil {
			return member, 0, domain.Err("INVALID_STATE", "An earlier enrollment decision must be reconciled.", 409)
		}
		member.Epoch = existing.Epoch
	} else if existing.State == "removed" {
		member.Epoch = existing.Epoch + 1
	} else {
		return member, 0, domain.Err("INVALID_STATE", "Node already has active membership.", 409)
	}
	member.Version = existing.Version + 1
	data, _ = json.Marshal(member)
	revision, err = kv.Update("member."+member.NodeID, data, entry.Revision())
	if err != nil {
		return member, 0, unavailable()
	}
	return member, revision, nil
}
func (m *Manager) leafEndpoints() ([]domain.Endpoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.broker == nil {
		return nil, unavailable()
	}
	status, err := m.broker.Server.Varz(nil)
	if err != nil {
		return nil, unavailable()
	}
	addresses, err := network.BindAddresses(m.config)
	if err != nil {
		return nil, domain.Err("UNAVAILABLE", "Issuer management endpoints are unavailable.", 503)
	}
	endpoints := []domain.Endpoint{}
	for _, address := range addresses {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			continue
		}
		endpoints = append(endpoints, domain.Endpoint{Service: "nats_leaf", Transport: "tls", Address: net.JoinHostPort(host, strconv.Itoa(status.LeafNode.Port))})
	}
	return endpoints, nil
}
func loadLeafGrants(ctx context.Context, kv nats.KeyValue, b *broker.Broker) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	keys, err := kv.Keys()
	if errors.Is(err, nats.ErrNoKeysFound) {
		return nil
	}
	if err != nil || len(keys) > domain.MaxRecords+domain.QueueCapacity {
		return unavailable()
	}
	leaves := map[string]broker.LeafGrant{}
	for _, key := range keys {
		if ctx.Err() != nil {
			return unavailable()
		}
		if !strings.HasPrefix(key, "member.") {
			continue
		}
		entry, err := kv.Get(key)
		if err != nil {
			return unavailable()
		}
		var member Member
		if domain.StrictJSON(entry.Value(), &member) != nil {
			return unavailable()
		}
		if member.BrokerRole != "leaf" || (member.State != "pending" && member.State != "full" && member.State != "enrolling") {
			continue
		}
		if member.State == "enrolling" {
			record, _, err := readJoin(kv, member.RequestID)
			if err != nil {
				return unavailable()
			}
			if record.View.State != "approved" || record.Issued == nil || record.Issued.Epoch != member.Epoch {
				continue
			}
		}
		leaves[member.BrokerKey] = broker.LeafGrant{NodeID: member.NodeID}
		if len(leaves) > 256 {
			return domain.Err("QUEUE_FULL", "Pending leaf grant capacity is exhausted.", 503)
		}
	}
	return b.AllowLeaves(leaves)
}
