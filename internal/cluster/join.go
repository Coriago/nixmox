package cluster

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/machine"
	"nixmox/internal/pki"
	"nixmox/internal/service"
)

type joinPayload struct {
	Address             string `json:"address"`
	ExpectedClusterName string `json:"expected_cluster_name"`
}
type resolvedJoin struct {
	Address string                   `json:"address"`
	Request domain.EnrollmentRequest `json:"request"`
}
type joinCheckpoint struct {
	ServerCertificate string              `json:"server_certificate"`
	Deadline          time.Time           `json:"deadline"`
	Request           *domain.JoinRequest `json:"request,omitempty"`
	Issued            *domain.Issuance    `json:"issued,omitempty"`
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	n, e := strconv.Atoi(port)
	return err == nil && e == nil && n > 0 && n < 65536 && len(host) > 0 && len(host) <= 253 && !strings.ContainsAny(host, "/@?#\\ \t\r\n%")
}
func (m *Manager) joinOperation() service.Operation {
	return service.Operation{Kind: "cluster.join", Scope: "node_local", Timeout: 12 * time.Minute, SafeRetry: true,
		Validate: func(c domain.Command) error {
			var p joinPayload
			if len(c.ExpectedVersions) != 0 || domain.StrictJSON(c.Payload, &p) != nil || !validAddress(p.Address) || !clusterName.MatchString(p.ExpectedClusterName) {
				return domain.Err("INVALID_PAYLOAD", "A machine address (host:port) and expected cluster name are required.", 422)
			}
			return nil
		}, Resolve: func(ctx context.Context, c domain.Command) (json.RawMessage, error) {
			m.mu.RLock()
			defer m.mu.RUnlock()
			if m.identity.ClusterID != nil {
				return nil, domain.Err("INVALID_STATE", "Joining requires an unjoined node.", 409)
			}
			var p joinPayload
			json.Unmarshal(c.Payload, &p)
			daemon, err := pki.CSR(m.keys.Daemon)
			if err != nil {
				return nil, err
			}
			broker, err := pki.CSR(m.keys.Broker)
			if err != nil {
				return nil, err
			}
			name, err := os.Hostname()
			if err != nil || name == "" || len(name) > 128 {
				name = m.identity.NodeID
			}
			return json.Marshal(resolvedJoin{Address: p.Address, Request: domain.EnrollmentRequest{ID: domain.NewID("join_"), NodeID: m.identity.NodeID, Name: name, ExpectedClusterName: p.ExpectedClusterName, DaemonCSR: string(daemon), BrokerCSR: string(broker)}})
		}, Exclusions: func(c domain.Command, _ json.RawMessage) ([]string, error) {
			return []string{"identity:" + c.TargetID}, nil
		}, Run: m.join, Reconcile: m.join}
}

func (m *Manager) join(ctx context.Context, e *service.Execution) (any, error) {
	var pinned resolvedJoin
	if domain.StrictJSON(e.Resolved, &pinned) != nil || !validAddress(pinned.Address) || !domain.SafeID.MatchString(pinned.Request.ID) {
		return nil, domain.Err("INVALID_STATE", "Pinned enrollment request unavailable.", 409)
	}
	m.mu.RLock()
	keys := m.keys
	conflict := m.membership != nil && m.membership.JoinRequestID != pinned.Request.ID
	m.mu.RUnlock()
	if conflict {
		return nil, domain.Err("INVALID_STATE", "This node has a different cluster association.", 409)
	}
	snapshot, err := e.Snapshot()
	if err != nil {
		return nil, err
	}
	var cp joinCheckpoint
	readErr := m.store.Read("enrollment/"+pinned.Request.ID+".json", &cp)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	if errors.Is(readErr, os.ErrNotExist) && len(snapshot.Checkpoint) > 0 && domain.StrictJSON(snapshot.Checkpoint, &cp) != nil {
		return nil, domain.Err("INVALID_STATE", "Enrollment checkpoint unavailable.", 409)
	}
	if cp.ServerCertificate == "" {
		cert, err := machine.Probe(ctx, pinned.Address, keys.Daemon)
		if err != nil {
			return nil, enrollmentError(err)
		}
		cp.ServerCertificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
		cp.Deadline = time.Now().UTC().Add(11 * time.Minute)
		if err = m.saveJoinCheckpoint(e, pinned.Request.ID, "requesting_enrollment", cp, true); err != nil {
			return nil, err
		}
	}
	server, err := parseCertificate(cp.ServerCertificate)
	if err != nil {
		return nil, err
	}
	client, err := machine.EnrollmentClient(pinned.Address, keys.Daemon, pki.Fingerprint(server.PublicKey))
	if err != nil {
		return nil, err
	}
	defer client.Close()
	for cp.Issued == nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		expired := time.Now().After(cp.Deadline)
		var view domain.JoinRequest
		if cp.Request == nil && !expired {
			view, err = client.Submit(ctx, pinned.Request)
		} else {
			view, err = client.Read(ctx, pinned.Request.ID)
		}
		if err == nil {
			if view.ID != pinned.Request.ID || view.NodeID != pinned.Request.NodeID || view.KeyFingerprint != pki.Fingerprint(keys.Daemon.Leaf.PublicKey) || !domain.SafeID.MatchString(view.ClusterID) || len(view.ClusterFingerprint) != 64 || (cp.Request != nil && (cp.Request.ClusterID != view.ClusterID || cp.Request.ClusterFingerprint != view.ClusterFingerprint)) {
				return nil, domain.Err("IDENTITY_CONFLICT", "Enrollment response does not match the pinned request.", 409)
			}
			if cp.Request == nil || cp.Request.Version != view.Version {
				cp.Request = &view
				if err = m.saveJoinCheckpoint(e, pinned.Request.ID, "awaiting_approval", cp, true); err != nil {
					return nil, err
				}
			}
			switch view.State {
			case "approved":
				issued, issueErr := client.Certificates(ctx, pinned.Request.ID)
				err = issueErr
				if err == nil {
					if err = validateIssuance(issued, pinned, cp, keys); err != nil {
						return nil, err
					}
					cp.Issued = &issued
					if err = m.saveJoinCheckpoint(e, pinned.Request.ID, "installing_membership", cp, false); err != nil {
						return nil, err
					}
				}
			case "rejected", "expired":
				return nil, domain.Err("ENROLLMENT_"+strings.ToUpper(view.State), "The join request is "+view.State+"; start a new explicit join attempt.", 409)
			case "awaiting_approval":
			default:
				return nil, domain.Err("INVALID_RESPONSE", "Unknown enrollment state.", 502)
			}
		}
		if err != nil {
			var problem *domain.Error
			if errors.As(err, &problem) && problem.Status >= 400 && problem.Status < 500 {
				return nil, enrollmentError(err)
			}
		}
		if expired && cp.Issued == nil {
			return nil, domain.Err("ENROLLMENT_TIMEOUT", "Enrollment could not be reconciled before its deadline; retry this task to inspect the same request.", 503)
		}
		if cp.Issued == nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	if err = validateIssuance(*cp.Issued, pinned, cp, keys); err != nil {
		return nil, err
	}
	if err = e.Checkpoint("installing_membership", cp, false); err != nil {
		return nil, err
	}
	if err = m.installMembership(*cp.Issued, pinned, e.Command.ID); err != nil {
		return nil, err
	}
	if err = e.RefreshIdentity(); err != nil {
		return nil, err
	}
	return map[string]any{"cluster_id": cp.Issued.ClusterID, "request_id": pinned.Request.ID, "membership": "pending", "broker_role": "leaf"}, nil
}

func (m *Manager) saveJoinCheckpoint(e *service.Execution, id, phase string, cp joinCheckpoint, cancellable bool) error {
	if err := os.MkdirAll(filepath.Join(m.store.Root, "enrollment"), 0700); err != nil {
		return err
	}
	if err := m.store.Write("enrollment/"+id+".json", cp); err != nil {
		return err
	}
	return e.Checkpoint(phase, cp, cancellable)
}

func enrollmentError(err error) error {
	var p *domain.Error
	if errors.As(err, &p) {
		// Peer error text is not trusted as a local diagnostic.
		return domain.Err(p.Code, "Enrollment failed ("+p.Code+").", p.Status)
	}
	return domain.Err("ENROLLMENT_UNAVAILABLE", "The enrollment endpoint could not be reached or authenticated.", 503)
}
func parseCertificate(value string) (*x509.Certificate, error) {
	b, rest := pem.Decode([]byte(value))
	if b == nil || b.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("invalid certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}
func validateIssuance(i domain.Issuance, pinned resolvedJoin, cp joinCheckpoint, keys pki.Keys) error {
	bad := domain.Err("IDENTITY_CONFLICT", "Issued certificates do not match the approved enrollment identity.", 409)
	if cp.Request == nil || i.RequestID != pinned.Request.ID || i.NodeID != pinned.Request.NodeID || i.ClusterName != pinned.Request.ExpectedClusterName || i.ClusterID != cp.Request.ClusterID || !domain.SafeID.MatchString(i.IssuerNodeID) || i.Epoch < 1 || i.IssuerEpoch < 1 {
		return bad
	}
	ca, err := parseCertificate(i.CA)
	if err != nil || !ca.IsCA || ca.Subject.CommonName != i.ClusterID || pki.Fingerprint(ca.PublicKey) != cp.Request.ClusterFingerprint {
		return bad
	}
	verify := func(value, node, role, key string, epoch int) error {
		cert, err := parseCertificate(value)
		if err != nil || !pki.Bound(cert, i.ClusterID, node, role, epoch) || (key != "" && pki.Fingerprint(cert.PublicKey) != key) {
			return bad
		}
		_, err = cert.Verify(x509.VerifyOptions{Roots: pki.Pool(ca), DNSName: role + ".nixmox", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}})
		if err != nil {
			return bad
		}
		return nil
	}
	if verify(i.DaemonCertificate, i.NodeID, "daemon", pki.Fingerprint(keys.Daemon.Leaf.PublicKey), i.Epoch) != nil || verify(i.BrokerCertificate, i.NodeID, "broker", pki.Fingerprint(keys.Broker.Leaf.PublicKey), i.Epoch) != nil || verify(i.IssuerBrokerCertificate, i.IssuerNodeID, "broker", "", i.IssuerEpoch) != nil {
		return bad
	}
	server, err := parseCertificate(cp.ServerCertificate)
	if err != nil || verify(cp.ServerCertificate, i.IssuerNodeID, "daemon", pki.Fingerprint(server.PublicKey), i.IssuerEpoch) != nil {
		return bad
	}
	if len(i.Endpoints) == 0 || len(i.Endpoints) > 64 {
		return bad
	}
	for _, ep := range i.Endpoints {
		if ep.Service != "nats_leaf" || ep.Transport != "tls" || !validAddress(ep.Address) {
			return bad
		}
	}
	return nil
}

func (m *Manager) installMembership(i domain.Issuance, pinned resolvedJoin, commandID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var prior Membership
	err := m.store.Read("trust/membership.json", &prior)
	if err == nil && (prior.ClusterID != i.ClusterID || prior.JoinRequestID != i.RequestID || prior.NodeID != i.NodeID) {
		return domain.Err("IDENTITY_CONFLICT", "An existing cluster association must be reconciled first.", 409)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if m.identity.ClusterID != nil && (*m.identity.ClusterID != i.ClusterID || m.identity.MembershipEpoch != i.Epoch) {
		return domain.Err("IDENTITY_CONFLICT", "Membership epoch changed during enrollment.", 409)
	}
	// Prefer the issuer's reachable machine address when it advertises a leaf
	// endpoint on that host. Loopback endpoints are only suitable for local peers.
	host, _, _ := net.SplitHostPort(pinned.Address)
	endpoint := i.Endpoints[0].Address
	for _, ep := range i.Endpoints {
		h, _, _ := net.SplitHostPort(ep.Address)
		if h == host {
			endpoint = ep.Address
			break
		}
		ip := net.ParseIP(h)
		if ip != nil && !ip.IsLoopback() {
			endpoint = ep.Address
		}
	}
	leafURL := (&url.URL{Scheme: "tls", Host: endpoint}).String()
	membership := Membership{SchemaVersion: 1, ClusterID: i.ClusterID, Name: i.ClusterName, NodeID: i.NodeID, IssuerNodeID: i.IssuerNodeID, Epoch: i.Epoch, Replicas: 1, JoinRequestID: i.RequestID, JoinCommandID: commandID, CA: i.CA, DaemonCertificate: i.DaemonCertificate, BrokerCertificate: i.BrokerCertificate, IssuerBrokerCertificate: i.IssuerBrokerCertificate, LeafURL: leafURL}
	if err = m.prepareTrustDirectory(); err != nil {
		return err
	}
	if err = m.store.Write("trust/membership.json", membership); err != nil {
		return err
	}
	for path, value := range map[string]string{"trust/ca.pem": i.CA, "pki/daemon/cert.pem": i.DaemonCertificate, "pki/broker/cert.pem": i.BrokerCertificate} {
		if err = m.store.WriteBytes(path, []byte(value)); err != nil {
			return err
		}
	}
	keys, err := pki.Open(m.store)
	if err != nil {
		return err
	}
	if m.broker != nil {
		m.broker.Close()
		m.broker = nil
		m.stores = nil
	}
	m.keys = keys
	m.membership = &membership
	if err = m.startBroker(); err != nil {
		return err
	}
	identity := m.identity
	if identity.ClusterID == nil {
		identity.ClusterID = &membership.ClusterID
		identity.MembershipEpoch = i.Epoch
		identity.Version++
		ca, _ := parseCertificate(i.CA)
		fingerprint := pki.Fingerprint(ca.PublicKey)
		identity.ClusterFingerprint = &fingerprint
		if err = m.store.Write("identity.json", identity); err != nil {
			return err
		}
		m.identity = identity
	}
	return nil
}
