package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sync"

	"github.com/nats-io/nats-server/v2/server"
	js "github.com/nats-io/nats.go/jetstream"
	"golang.org/x/time/rate"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/comms"
	"nixmox/internal/engine/identity"
	"nixmox/internal/runtime"
)

// ClusterNode owns membership policy. Transport generations own their clients,
// consumers and broker; inventory observations never authorize membership.
type ClusterNode struct {
	mu           sync.Mutex
	life         context.Context
	scope        context.Context
	cancelScope  context.CancelFunc
	scopeCluster domain.ID
	scopeEpoch   uint64
	config       runtime.Config
	keys         *identity.Local
	host         domain.Host
	transport    *comms.Runtime
	state        *domain.MembershipState
	revision     engine.Revision
	inventory    *inventory
	limiter      *rate.Limiter
}

func OpenCluster(ctx context.Context, config runtime.Config, keys *identity.Local, host domain.Host) (*ClusterNode, error) {
	c := &ClusterNode{life: ctx, config: config, keys: keys, host: host, transport: &comms.Runtime{}, inventory: newInventory(), limiter: rate.NewLimiter(5, 10)}
	if err := c.configure(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (c *ClusterNode) Close() { c.mu.Lock(); defer c.mu.Unlock(); c.transport.Close() }
func (c *ClusterNode) creator() bool {
	return c.keys.Trust != nil && c.keys.Trust.Cluster.IssuerNodeID == c.keys.Identity.NodeID
}
func (c *ClusterNode) Identity() domain.Identity {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keys.Identity
}
func (c *ClusterNode) leafAddress() string {
	address := c.host.Endpoints[0].Address
	for _, endpoint := range c.host.Endpoints {
		host, _, _ := net.SplitHostPort(endpoint.Address)
		if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
			address = endpoint.Address
			break
		}
	}
	host, _, _ := net.SplitHostPort(address)
	return net.JoinHostPort(host, fmt.Sprint(c.config.NATS.LeafPort))
}

func (c *ClusterNode) brokerOptions() (comms.BrokerOptions, error) {
	id := c.keys.Identity.NodeID
	opts := comms.BrokerOptions{NodeID: id, ClusterID: id, DataDir: filepath.Join(c.config.DataDir, "broker"), MaxStoreBytes: 64 << 20,
		ClientAddress: net.JoinHostPort("127.0.0.1", fmt.Sprint(c.config.NATS.ClientPort)), ClientTLS: c.keys.ServerTLS(), DaemonKeys: []domain.Digest{c.keys.DaemonKey()}}
	trust := c.keys.Trust
	if trust == nil {
		return opts, nil
	}
	opts.ClusterID, opts.Pending = trust.Cluster.ID, !c.creator()
	if err := c.routeOptions(&opts); err != nil {
		return opts, err
	}
	if c.creator() {
		// Remote listeners stay closed until committed membership has been read.
		if c.state != nil {
			opts.LeafAddress, opts.LeafTLS = c.leafAddress(), c.keys.PeerTLS(id)
			opts.Leaves = map[domain.ID]domain.Digest{}
			opts.LeafCertificates = map[domain.ID]domain.Digest{}
			for node, member := range c.state.Members {
				if node != id && !member.Removed {
					if member.BrokerCertificate == "" {
						return opts, errors.New("membership certificate binding is missing")
					}
					opts.Leaves[node] = member.BrokerKey
					opts.LeafCertificates[node] = member.BrokerCertificate
				}
			}
		}
		return opts, nil
	}
	if !opts.Pending {
		return opts, nil
	}
	remote, err := url.Parse("tls://" + trust.LeafAddress)
	if err != nil || remote.Hostname() == "" || remote.Port() == "" {
		return opts, errors.New("invalid pinned leaf address")
	}
	tlsConfig, err := comms.PinnedTLS(c.keys.PeerTLS(trust.Cluster.IssuerNodeID), []domain.Digest{trust.BrokerKey})
	if err != nil {
		return opts, err
	}
	opts.LeafRemotes = []*server.RemoteLeafOpts{{URLs: []*url.URL{remote}, LocalAccount: "NX", TLSConfig: tlsConfig, TLSHandshakeFirst: true, NoRandomize: true, IgnoreDiscoveredServers: true}}
	return opts, nil
}

func (c *ClusterNode) startTransport() error {
	opts, err := c.brokerOptions()
	if err != nil {
		return err
	}
	var limits *comms.StorageLimits
	if c.creator() {
		limits = &comms.StorageLimits{Commands: 16 << 20, Results: 16 << 20, State: 8 << 20, Git: 8 << 20, Replicas: 1, Existing: c.keys.Trust.ControlInitialized}
	}
	transport, err := comms.OpenRuntime(c.life, opts, c.keys.ClientTLS(), limits)
	if err == nil {
		c.transport = transport
	}
	return err
}

// Some NATS leaf authorization changes require a restart. Only the transport
// generation changes; node identity, membership and execution history do not.
func (c *ClusterNode) configure(ctx context.Context) error {
	c.syncMembershipContext()
	c.transport.Close()
	if err := c.startTransport(); err != nil {
		return err
	}
	if c.creator() && c.state == nil {
		if err := c.loadMembership(ctx); err != nil {
			return err
		}
		c.transport.Close()
		if err := c.startTransport(); err != nil {
			return err
		}
	}
	if c.creator() {
		core := c.transport.Core
		return core.ObservePresence(func(h domain.Heartbeat) { c.acceptHeartbeat(core, h) }, func(s domain.HostTelemetry) { c.acceptSample(core, s) })
	}
	return nil
}

func (c *ClusterNode) loadMembership(ctx context.Context) error {
	trust := c.keys.Trust
	id := c.keys.Identity.NodeID
	entry, err := c.transport.Store.Get(ctx, "membership")
	if errors.Is(err, js.ErrKeyNotFound) && !trust.ControlInitialized {
		state := domain.MembershipState{Topology: trust.Topology, Cluster: trust.Cluster, Members: map[domain.ID]domain.Member{id: {NodeID: id, Version: 1, Epoch: 1, DaemonKey: c.keys.DaemonKey(), BrokerKey: c.keys.BrokerKey(), BrokerCertificate: c.keys.BrokerCertificate(), RequestID: trust.Issuance.RequestID}}, Requests: map[domain.ID]domain.JoinRequest{}}
		if err = c.saveState(ctx, state); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("cluster membership unavailable: %w", err)
	} else {
		var state domain.MembershipState
		if err = json.Unmarshal(entry.Value, &state); err != nil {
			return err
		}
		if state.Cluster.ID != trust.Cluster.ID || state.Members[id].NodeID != id {
			return errors.New("cluster membership identity mismatch")
		}
		c.state, c.revision = &state, entry.Revision
	}
	if err := c.completeMembership(ctx); err != nil {
		return err
	}
	if !trust.ControlInitialized {
		copy := *trust
		copy.ControlInitialized = true
		return c.keys.Install(copy)
	}
	return nil
}
func (c *ClusterNode) quorum(ctx context.Context) error {
	if !c.creator() || c.transport.Store == nil || c.transport.Context.Err() != nil {
		return &domain.Error{Code: "QUORUM_UNAVAILABLE", Message: "Cluster authority is unavailable"}
	}
	q, err := c.transport.Store.Quorum(ctx)
	if err != nil || !q.Available {
		return &domain.Error{Code: "QUORUM_UNAVAILABLE", Message: "Durable cluster quorum is unavailable", Retryable: true}
	}
	return nil
}
func (c *ClusterNode) saveState(ctx context.Context, state domain.MembershipState) error {
	if err := c.quorum(ctx); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	revision, err := c.transport.Store.CompareAndSwap(ctx, "membership", c.revision, data)
	if err == nil {
		c.state, c.revision = &state, revision
	}
	return err
}
