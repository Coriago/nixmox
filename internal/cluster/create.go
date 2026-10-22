package cluster

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"regexp"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/pki"
	"nixmox/internal/service"
)

var clusterName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func parseCA(block *pem.Block) (*x509.Certificate, error) {
	if block == nil {
		return nil, errors.New("missing cluster authority")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA {
		return nil, errors.New("invalid cluster authority")
	}
	return ca, nil
}

type createPayload struct {
	Name string `json:"name"`
}
type resolvedCreate struct {
	Name      string `json:"name"`
	ClusterID string `json:"cluster_id"`
}

func (m *Manager) Operations() []service.Operation {
	run := func(ctx context.Context, e *service.Execution) (any, error) {
		var pinned resolvedCreate
		if domain.StrictJSON(e.Resolved, &pinned) != nil || !domain.SafeID.MatchString(pinned.ClusterID) {
			return nil, domain.Err("INVALID_STATE", "Pinned cluster genesis is unavailable.", 409)
		}
		if err := e.Checkpoint("genesis", pinned, false); err != nil {
			return nil, err
		}
		result, err := m.create(ctx, e.Command.ID, pinned)
		if err == nil {
			err = e.RefreshIdentity()
		}
		return result, err
	}
	return []service.Operation{{Kind: "cluster.create", Scope: "node_local", Timeout: 30 * time.Second, SafeRetry: true, Validate: func(c domain.Command) error {
		var p createPayload
		if len(c.ExpectedVersions) != 0 || domain.StrictJSON(c.Payload, &p) != nil || !clusterName.MatchString(p.Name) {
			return domain.Err("INVALID_PAYLOAD", "A cluster name is required; expected versions are not accepted.", 422)
		}
		return nil
	}, Resolve: func(ctx context.Context, c domain.Command) (json.RawMessage, error) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		if m.identity.ClusterID != nil {
			return nil, domain.Err("INVALID_STATE", "Cluster creation requires an unjoined node.", 409)
		}
		var p createPayload
		json.Unmarshal(c.Payload, &p)
		return json.Marshal(resolvedCreate{Name: p.Name, ClusterID: domain.NewID("c_")})
	}, Exclusions: func(c domain.Command, r json.RawMessage) ([]string, error) {
		return []string{"identity:" + c.TargetID}, nil
	}, Run: run, Reconcile: run}}
}
func (m *Manager) create(ctx context.Context, commandID string, pinned resolvedCreate) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.identity.ClusterID != nil && (*m.identity.ClusterID != pinned.ClusterID || m.membership.GenesisCommandID != commandID) {
		return nil, domain.Err("INVALID_STATE", "This node already belongs to a cluster.", 409)
	}
	if err := m.prepareTrustDirectory(); err != nil {
		return nil, err
	}
	var membership Membership
	err := m.store.Read("trust/membership.json", &membership)
	if err == nil {
		if membership.ClusterID != pinned.ClusterID || membership.GenesisCommandID != commandID || membership.NodeID != m.identity.NodeID {
			return nil, domain.Err("RECONCILIATION_REQUIRED", "A previous cluster genesis must be reconciled first.", 409)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		a, err := pki.OpenAuthority(m.store, pinned.ClusterID, true)
		if err != nil {
			return nil, err
		}
		daemonCSR, err := pki.CSR(m.keys.Daemon)
		if err != nil {
			return nil, err
		}
		daemonRequest, err := pki.ParseCSR(daemonCSR)
		if err != nil {
			return nil, err
		}
		brokerCSR, err := pki.CSR(m.keys.Broker)
		if err != nil {
			return nil, err
		}
		brokerRequest, err := pki.ParseCSR(brokerCSR)
		if err != nil {
			return nil, err
		}
		epoch := m.identity.MembershipEpoch + 1
		daemonCert, err := a.Issue(daemonRequest, m.identity.NodeID, "daemon", epoch)
		if err != nil {
			return nil, err
		}
		brokerCert, err := a.Issue(brokerRequest, m.identity.NodeID, "broker", epoch)
		if err != nil {
			return nil, err
		}
		membership = Membership{SchemaVersion: 1, ClusterID: pinned.ClusterID, Name: pinned.Name, NodeID: m.identity.NodeID, IssuerNodeID: m.identity.NodeID, Epoch: epoch, Replicas: 1, Provisional: true, GenesisCommandID: commandID, CA: string(a.PEM), DaemonCertificate: string(daemonCert), BrokerCertificate: string(brokerCert)}
		// This public issuance ledger pins certificates before any external response.
		if err = m.store.Write("trust/membership.json", membership); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if err := m.store.WriteBytes("trust/ca.pem", []byte(membership.CA)); err != nil {
		return nil, err
	}
	if err := m.store.WriteBytes("pki/daemon/cert.pem", []byte(membership.DaemonCertificate)); err != nil {
		return nil, err
	}
	if err := m.store.WriteBytes("pki/broker/cert.pem", []byte(membership.BrokerCertificate)); err != nil {
		return nil, err
	}
	keys, err := pki.Open(m.store)
	if err != nil {
		return nil, err
	}
	m.keys = keys
	m.membership = &membership
	if m.broker != nil {
		m.broker.Close()
		m.broker = nil
		m.stores = nil
	}
	if err := m.startBroker(); err != nil {
		return nil, err
	}
	genesis := Genesis{SchemaVersion: 1, Version: 1, ID: membership.ClusterID, Name: membership.Name, IssuerNodeID: m.identity.NodeID, CommandID: commandID, Replicas: 1}
	genesis.Creator.NodeID = m.identity.NodeID
	genesis.Creator.Membership = "pending"
	genesis.Creator.Epoch = membership.Epoch
	genesis.Creator.DaemonKey = pki.Fingerprint(keys.Daemon.Leaf.PublicKey)
	genesis.Creator.BrokerKey = pki.Fingerprint(keys.Broker.Leaf.PublicKey)
	if err = persistExact(m.stores.State, "cluster", genesis); err != nil {
		return nil, err
	}
	identity := m.identity
	if identity.ClusterID == nil {
		identity.ClusterID = &membership.ClusterID
		identity.MembershipEpoch = membership.Epoch
		identity.Version++
		block, _ := pem.Decode([]byte(membership.CA))
		ca, err := parseCA(block)
		if err != nil {
			return nil, err
		}
		fingerprint := pki.Fingerprint(ca.PublicKey)
		identity.ClusterFingerprint = &fingerprint
		if err = m.store.Write("identity.json", identity); err != nil {
			return nil, err
		}
		m.identity = identity
	}
	return map[string]any{"cluster_id": membership.ClusterID, "membership": "pending", "broker_role": "provisional", "genesis_committed": true}, nil
}
