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
	m := &Manager{store: st, config: c, identity: id, keys: keys, querySlots: make(chan struct{}, 8)}
	if id.ClusterID != nil {
		var membership Membership
		if st.Read("trust/membership.json", &membership) != nil || membership.ClusterID != *id.ClusterID || membership.NodeID != id.NodeID {
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
	m.mu.Lock()
	d, b, q := m.dispatcher, m.broker, m.querySubscription
	m.dispatcher = nil
	m.broker = nil
	m.stores = nil
	m.querySubscription = nil
	m.closed = true
	m.mu.Unlock()
	if d != nil {
		d.Close()
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return unavailable()
	}
	if m.service == nil || m.stores == nil || m.identity.ClusterID == nil || m.dispatcher != nil {
		return nil
	}
	d, err := newDispatcher(m.stores, m.service, *m.identity.ClusterID, m.identity.NodeID)
	if err != nil {
		return err
	}
	m.dispatcher = d
	return m.startQueries()
}
func (m *Manager) Submit(c domain.Command) (domain.Receipt, error) {
	m.mu.RLock()
	d := m.dispatcher
	own := m.identity.NodeID
	m.mu.RUnlock()
	if d == nil {
		return domain.Receipt{}, unavailable()
	}
	if c.TargetID != own {
		return domain.Receipt{}, domain.Err("TARGET_UNREACHABLE", "Target is not an admitted executor.", 503)
	}
	return d.Submit(c)
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
		return domain.Receipt{}, unavailable()
	}
	return d.Receipt(id)
}
func (m *Manager) Certificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pair := m.keys.Daemon
	return &pair, nil
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
		return domain.ClusterStatus{Membership: "unjoined", ObservedAt: now, Freshness: "live", BrokerRole: "standalone"}, nil
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
	return domain.ClusterStatus{ClusterID: &genesis.ID, Name: &genesis.Name, Membership: "pending", ObservedAt: now, Freshness: "live", Quorum: true, BrokerRole: "provisional", IssuerNodeID: &genesis.IssuerNodeID}, nil
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
