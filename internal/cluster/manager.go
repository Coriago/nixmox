// Package cluster owns membership, enrollment and durable control-plane state.
package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/broker"
	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/pki"
	"nixmox/internal/service"
	"nixmox/internal/storage"
)

type Membership struct {
	SchemaVersion           int    `json:"schema_version"`
	ClusterID               string `json:"cluster_id"`
	Name                    string `json:"name"`
	NodeID                  string `json:"node_id"`
	IssuerNodeID            string `json:"issuer_node_id"`
	Epoch                   int    `json:"membership_epoch"`
	Replicas                int    `json:"replicas"`
	Provisional             bool   `json:"provisional"`
	GenesisCommandID        string `json:"genesis_command_id"`
	CA                      string `json:"ca"`
	DaemonCertificate       string `json:"daemon_certificate"`
	BrokerCertificate       string `json:"broker_certificate"`
	JoinRequestID           string `json:"join_request_id,omitempty"`
	JoinCommandID           string `json:"join_command_id,omitempty"`
	IssuerBrokerCertificate string `json:"issuer_broker_certificate,omitempty"`
	IssuerEpoch             int    `json:"issuer_epoch,omitempty"`
	LeafURL                 string `json:"leaf_url,omitempty"`
}
type Genesis struct {
	SchemaVersion int    `json:"schema_version"`
	Version       int64  `json:"version"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	IssuerNodeID  string `json:"issuer_node_id"`
	CommandID     string `json:"command_id"`
	Replicas      int    `json:"replicas"`
	Creator       struct {
		NodeID     string `json:"node_id"`
		Membership string `json:"membership"`
		Epoch      int    `json:"membership_epoch"`
		DaemonKey  string `json:"daemon_key"`
		BrokerKey  string `json:"broker_key"`
	} `json:"creator"`
}
type Manager struct {
	mu                sync.RWMutex
	store             *storage.Store
	config            config.Config
	identity          domain.Identity
	keys              pki.Keys
	membership        *Membership
	broker            *broker.Broker
	stores            *broker.Stores
	service           *service.Service
	dispatcher        *dispatcher
	joinRates         map[string]joinRate
	querySubscription *nats.Subscription
	querySlots        chan struct{}
	closed            bool
	transportMu       sync.Mutex
	agent             *executorAgent
	hub               *deliveryHub
	queryWorkers      sync.WaitGroup
	queryIngress      chan struct{}
}

func Open(st *storage.Store, c config.Config) (*Manager, error) {
	id, err := st.Identity()
	if err != nil {
		return nil, err
	}
	keys, err := pki.Open(st)
	if err != nil {
		return nil, err
	}
	m := &Manager{store: st, config: c, identity: id, keys: keys, querySlots: make(chan struct{}, 8), queryIngress: make(chan struct{}, 8)}
	if id.ClusterID != nil {
		var membership Membership
		if st.Read("trust/membership.json", &membership) != nil || membership.ClusterID != *id.ClusterID || membership.NodeID != id.NodeID || membership.Epoch != id.MembershipEpoch || id.ClusterFingerprint == nil {
			return nil, errors.New("cluster trust unavailable or inconsistent")
		}
		m.membership = &membership
	}
	if err := m.startBroker(); err != nil {
		return nil, err
	}
	return m, nil
}
func (m *Manager) Close() {
	m.transportMu.Lock()
	defer m.transportMu.Unlock()
	m.mu.Lock()
	d, b, q := m.dispatcher, m.broker, m.querySubscription
	agent, hub := m.agent, m.hub
	m.agent = nil
	m.hub = nil
	m.dispatcher = nil
	m.broker = nil
	m.stores = nil
	m.querySubscription = nil
	m.closed = true
	m.mu.Unlock()
	if agent != nil {
		agent.Close()
	}
	if hub != nil {
		hub.Close()
	}
	if d != nil {
		d.Close()
	}
	m.queryWorkers.Wait()
	if q != nil {
		q.Unsubscribe()
	}
	if b != nil {
		b.Close()
	}
}
func (m *Manager) Attach(s *service.Service) error {
	m.mu.Lock()
	m.service = s
	m.mu.Unlock()
	s.SetClusterTransport(m)
	return m.activateDispatcher()
}
func (m *Manager) activateDispatcher() error {
	m.transportMu.Lock()
	defer m.transportMu.Unlock()
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return unavailable()
	}
	if m.service == nil || m.membership == nil || m.identity.ClusterID == nil || m.agent != nil {
		m.mu.RUnlock()
		return nil
	}
	s, stores, b, membership := m.service, m.stores, m.broker, *m.membership
	m.mu.RUnlock()
	var d *dispatcher
	var hub *deliveryHub
	var err error
	if stores != nil {
		d, err = newDispatcher(stores, s, membership.ClusterID, membership.NodeID, m.member)
		if err != nil {
			return err
		}
		hub, err = newDeliveryHub(d, b.Client)
		if err != nil {
			d.Close()
			return err
		}
	}
	agent, err := newExecutorAgent(b.Client, s, membership)
	if err != nil {
		if hub != nil {
			hub.Close()
		}
		if d != nil {
			d.Close()
		}
		return err
	}
	m.mu.Lock()
	m.dispatcher = d
	m.hub = hub
	m.agent = agent
	err = m.startQueries()
	m.mu.Unlock()
	return err
}
func (m *Manager) Submit(c domain.Command) (domain.Receipt, error) {
	m.mu.RLock()
	d := m.dispatcher
	own, joined := m.identity.NodeID, m.identity.ClusterID != nil
	m.mu.RUnlock()
	if d == nil {
		if !joined {
			return domain.Receipt{}, unavailable()
		}
		if !pendingCommandAllowed(c, own) {
			return domain.Receipt{}, domain.Err("FORBIDDEN", "This mutation requires a full cluster gateway.", 403)
		}
		return m.pendingRequest("command.submit", c)
	}
	return d.Submit(c)
}

func (m *Manager) member(node string) (Member, error) {
	m.mu.RLock()
	if m.closed || m.stores == nil || m.membership == nil {
		m.mu.RUnlock()
		return Member{}, unavailable()
	}
	if node == m.identity.NodeID {
		member := Member{SchemaVersion: 1, NodeID: node, State: "pending", Epoch: m.membership.Epoch, BrokerRole: "provisional"}
		m.mu.RUnlock()
		return member, nil
	}
	kv := m.stores.State
	m.mu.RUnlock()
	if !domain.SafeID.MatchString(node) {
		return Member{}, domain.Err("INVALID_COMMAND", "Invalid target identity.", 422)
	}
	entry, err := kv.Get("member." + node)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return Member{}, domain.Err("TARGET_UNREACHABLE", "Target is not an admitted executor.", 503)
	}
	if err != nil {
		return Member{}, unavailable()
	}
	var member Member
	if domain.StrictJSON(entry.Value(), &member) != nil || member.NodeID != node || (member.State != "pending" && member.State != "full") {
		return Member{}, domain.Err("MEMBERSHIP_CHANGED", "Target has no active execution membership.", 409)
	}
	return member, nil
}
func (m *Manager) Receipt(id string) (domain.Receipt, error) {
	m.mu.RLock()
	d := m.dispatcher
	joined := m.identity.ClusterID != nil
	m.mu.RUnlock()
	if d == nil {
		if !joined {
			return domain.Receipt{}, domain.Err("NOT_FOUND", "Command not found.", 404)
		}
		return m.pendingRequest("command.receipt", map[string]string{"command_id": id})
	}
	return d.Receipt(id)
}
func (m *Manager) Certificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pair := m.keys.Daemon
	return &pair, nil
}
func (m *Manager) BrokerReady() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.closed && m.broker != nil && m.broker.Server.Running() && m.broker.Client.IsConnected()
}
func (m *Manager) TLS() *tls.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := pki.EnrollmentServer(m.keys.Daemon)
	c.Certificates = nil
	c.GetCertificate = m.Certificate
	return c
}
func (m *Manager) startBroker() error {
	if m.closed {
		return unavailable()
	}
	c := broker.Config{NodeID: m.identity.NodeID, DataDir: m.config.DataDir, Keys: m.keys, ClientPort: m.config.NATS.ClientPort, RoutePort: m.config.NATS.RoutePort, LeafPort: m.config.NATS.LeafPort}
	if membership := m.membership; membership != nil {
		block, _ := pem.Decode([]byte(membership.CA))
		if block == nil {
			return errors.New("invalid cluster authority")
		}
		ca, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !ca.IsCA {
			return errors.New("invalid cluster authority")
		}
		if membership.SchemaVersion != 1 || membership.Epoch < 1 || membership.Replicas != 1 {
			return errors.New("unsupported or invalid membership topology")
		}
		if m.identity.ClusterFingerprint != nil && *m.identity.ClusterFingerprint != pki.Fingerprint(ca.PublicKey) {
			return errors.New("cluster trust pin mismatch")
		}
		c.ClusterID = membership.ClusterID
		c.Roots = pki.Pool(ca)
		for role, certificate := range map[string]*x509.Certificate{"daemon": m.keys.Daemon.Leaf, "broker": m.keys.Broker.Leaf} {
			if !pki.Bound(certificate, membership.ClusterID, m.identity.NodeID, role, membership.Epoch) {
				return errors.New("membership certificate binding mismatch")
			}
			if _, err := certificate.Verify(x509.VerifyOptions{Roots: c.Roots, DNSName: role + ".nixmox"}); err != nil {
				return errors.New("membership certificate is unavailable or expired")
			}
		}
		if membership.Provisional && membership.IssuerNodeID == m.identity.NodeID {
			c.ControlPlane = true
			c.Provisional = true
			c.FullBrokers = map[string]bool{pki.Fingerprint(m.keys.Broker.Leaf.PublicKey): true}
		} else {
			peer, err := parseCertificate(membership.IssuerBrokerCertificate)
			remote, urlErr := url.Parse(membership.LeafURL)
			if membership.Provisional || !domain.SafeID.MatchString(membership.JoinRequestID) || err != nil || urlErr != nil || remote.Scheme != "tls" || !validAddress(remote.Host) || remote.User != nil || remote.Path != "" || remote.RawQuery != "" || remote.Fragment != "" {
				return errors.New("invalid pending membership")
			}
			if !pki.Bound(peer, membership.ClusterID, membership.IssuerNodeID, "broker", membership.IssuerEpoch) {
				return errors.New("issuer broker binding mismatch")
			}
			if _, err := peer.Verify(x509.VerifyOptions{Roots: c.Roots, DNSName: "broker.nixmox"}); err != nil {
				return errors.New("issuer broker certificate unavailable")
			}
			c.FullBrokers = map[string]bool{pki.Fingerprint(peer.PublicKey): true}
			c.RemoteLeaf = remote
		}
	}
	b, err := broker.Start(c)
	if err != nil {
		return err
	}
	m.broker = b
	if c.ControlPlane {
		m.stores, err = b.Stores(m.membership.Replicas)
		if err != nil {
			b.Close()
			m.broker = nil
			return err
		}
		if err = loadLeafGrants(context.Background(), m.stores.State, b); err != nil {
			b.Close()
			m.broker = nil
			return err
		}
	}
	return nil
}
func (m *Manager) Status() (domain.ClusterStatus, error) {
	m.mu.RLock()
	if m.membership != nil && !m.membership.Provisional && m.broker != nil {
		membership, nc := *m.membership, m.broker.Client
		m.mu.RUnlock()
		select {
		case m.querySlots <- struct{}{}:
			defer func() { <-m.querySlots }()
		default:
			return domain.ClusterStatus{}, unavailable()
		}
		return pendingStatus(nc, membership)
	}
	defer m.mu.RUnlock()
	now := time.Now().UTC()
	if m.membership == nil {
		return domain.ClusterStatus{Membership: "unjoined", ObservedAt: now, Freshness: "live", BrokerRole: "standalone", Health: domain.Health{Status: "unknown", Reasons: []domain.HealthReason{}}}, nil
	}
	if m.stores == nil {
		return domain.ClusterStatus{}, unavailable()
	}
	if m.identity.ClusterID == nil {
		return domain.ClusterStatus{}, unavailable()
	}
	entry, err := m.stores.State.Get("cluster")
	if err != nil {
		return domain.ClusterStatus{}, unavailable()
	}
	var genesis Genesis
	if json.Unmarshal(entry.Value(), &genesis) != nil {
		return domain.ClusterStatus{}, unavailable()
	}
	return domain.ClusterStatus{ClusterID: &genesis.ID, Name: &genesis.Name, Membership: "pending", ObservedAt: now, Freshness: "live", Quorum: domain.Quorum{Available: true, Replicas: genesis.Replicas, Required: genesis.Replicas/2 + 1}, Health: domain.Health{Status: "healthy", ObservedAt: &now, Reasons: []domain.HealthReason{}}, BrokerRole: "provisional", IssuerNodeID: &genesis.IssuerNodeID}, nil
}
func unavailable() *domain.Error {
	return domain.Err("QUORUM_UNAVAILABLE", "Authenticated cluster quorum is unavailable.", 503)
}
func persistExact(kv nats.KeyValue, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = kv.Create(key, b); err == nil {
		return nil
	}
	entry, readErr := kv.Get(key)
	if readErr != nil {
		return unavailable()
	}
	if string(entry.Value()) != string(b) {
		return domain.Err("IDENTITY_CONFLICT", "Durable cluster identity differs from the pinned genesis.", 409)
	}
	return nil
}
func (m *Manager) prepareTrustDirectory() error {
	return os.MkdirAll(filepath.Join(m.store.Root, "trust"), 0700)
}
