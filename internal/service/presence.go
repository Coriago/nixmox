package service

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/comms"
)

func (c *ClusterNode) localHost() domain.Host {
	host := c.host
	host.Version = c.keys.Identity.Version
	if c.state != nil {
		host.Version = c.state.Members[host.ID].Version
		host.Configuration = c.state.Members[host.ID].Configuration
	}
	now := time.Now().UTC()
	host.LastSeen = &now
	if trust := c.keys.Trust; trust != nil {
		host.ClusterID = &trust.Cluster.ID
		host.Membership, host.MembershipEpoch = domain.Pending, &trust.Issuance.MembershipEpoch
		host.PendingReasons = []string{"configuration_not_deployed"}
		host.BrokerRole = "leaf"
		if c.creator() {
			host.BrokerRole = "provisional"
		}
		if c.forced() {
			host.Membership = domain.Full
			host.BrokerRole = "route"
			host.PendingReasons = []string{"forced_without_deployment_verification"}
		}
		if !c.creator() && !c.forced() && (c.transport.Broker == nil || c.transport.Broker.LeafConnections() == 0) {
			host.Connectivity = []domain.Connectivity{{PeerID: trust.Cluster.IssuerNodeID, Service: "nats_leaf", State: "disconnected"}}
		} else {
			host.Connectivity = []domain.Connectivity{{PeerID: trust.Cluster.IssuerNodeID, Service: "nats_leaf", State: "connected"}}
		}
	}
	return host
}

func (c *ClusterNode) acceptHeartbeat(core *comms.Core, h domain.Heartbeat) {
	c.mu.Lock()
	if core != c.transport.Core || c.state == nil || h.Host.MembershipEpoch == nil || h.StreamID == "" {
		c.mu.Unlock()
		return
	}
	member, ok := c.state.Members[h.Host.ID]
	cluster, issuer := c.state.Cluster.ID, c.state.Cluster.IssuerNodeID
	c.mu.Unlock()
	if ok && !member.Removed && member.Epoch == *h.Host.MembershipEpoch {
		accepted, changed := c.inventory.heartbeat(h, member, cluster, issuer)
		if !accepted {
			return
		}
		if changed {
			c.signal()
		}
		c.mu.Lock()
		if reservation := c.state.Power; reservation != nil && reservation.HostID == h.Host.ID && reservation.Epoch == member.Epoch && h.BootID != "" && h.BootID != reservation.BootID {
			state := *c.state
			state.Power = nil
			ctx, cancel := context.WithTimeout(c.life, domain.ExportTimeout)
			_ = c.saveState(ctx, state)
			cancel()
		}
		c.mu.Unlock()
	}
}
func (c *ClusterNode) acceptSample(core *comms.Core, s domain.HostTelemetry) {
	c.mu.Lock()
	if core != c.transport.Core || c.state == nil || s.MembershipEpoch == nil {
		c.mu.Unlock()
		return
	}
	member, ok := c.state.Members[s.HostID]
	c.mu.Unlock()
	if ok && !member.Removed && member.Epoch == *s.MembershipEpoch {
		c.inventory.sample(s)
	}
}
func (c *ClusterNode) Hosts() []domain.Host {
	c.mu.Lock()
	local, state := c.localHost(), c.state
	c.mu.Unlock()
	if c.cache != nil {
		local.Cache = c.cache.Advertisement()
	}
	if state == nil {
		return []domain.Host{local}
	}
	return c.inventory.hosts(state.Cluster.ID, state.Members, local)
}

func (c *ClusterNode) Status(ctx context.Context) domain.ClusterStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := domain.ClusterStatus{Membership: domain.Unjoined, Observed: domain.Observed{Freshness: domain.Unknown}, Health: domain.Health{Status: "unknown", Reasons: []domain.HealthReason{}}}
	status.Broker = c.transport.Replication(ctx)
	if trust := c.keys.Trust; trust != nil {
		now := time.Now().UTC()
		status.ClusterID, status.Membership = &trust.Cluster.ID, domain.Pending
		status.Observed = domain.Observed{ObservedAt: &now, Freshness: domain.Live}
		status.Health = domain.Health{Status: "degraded", ObservedAt: &now, Reasons: []domain.HealthReason{{Code: "no_full_members", Resource: string(trust.Cluster.ID), Message: "No full members; one provisional durable broker"}}}
		if c.forced() {
			status.Membership = domain.Full
			status.Health.Reasons = []domain.HealthReason{{Code: "forced_membership", Resource: string(trust.Cluster.ID), Message: "Full membership forced without deployment verification"}}
		}
		if c.transport.Store != nil {
			if q, err := c.transport.Store.Quorum(ctx); err == nil {
				status.Quorum = q
			} else if trust.Topology != nil {
				n := trust.Topology.Topology.Replicas
				status.Quorum = domain.Quorum{Replicas: n, Required: n/2 + 1}
			}
		}
		if c.creator() && !status.Quorum.Available {
			status.Health.Status = "unhealthy"
			status.Health.Reasons = append(status.Health.Reasons, domain.HealthReason{Code: "quorum_unavailable", Resource: string(trust.Cluster.ID), Message: "Cluster mutations are blocked"})
		}
		if !c.creator() && !c.forced() {
			status.Health.Reasons = append(status.Health.Reasons, domain.HealthReason{Code: "non_voting_leaf", Resource: string(c.host.ID), Message: "Pending leaf; remote quorum is not locally authoritative"})
		}
	}
	return status
}

func (c *ClusterNode) Sample(id domain.ID) (domain.HostTelemetry, error) {
	c.mu.Lock()
	if c.state == nil {
		c.mu.Unlock()
		return domain.HostTelemetry{}, notFound()
	}
	member, ok := c.state.Members[id]
	c.mu.Unlock()
	if !ok || member.Removed {
		return domain.HostTelemetry{}, notFound()
	}
	return c.inventory.current(id, member.Epoch), nil
}

func (c *ClusterNode) Telemetry(ctx context.Context, id domain.ID) (engine.Subscription[domain.HostTelemetry], error) {
	if c.leaf() {
		if _, err := c.call(ctx, "", "read", ReadRequest{Collection: "hosts", ID: &id, Subresource: "telemetry"}, true); err != nil {
			return nil, err
		}
		life, cancel := context.WithCancel(ctx)
		return &remoteTelemetry{cluster: c, host: id, interval: time.Duration(c.config.Telemetry.Host.FastIntervalMS) * time.Millisecond, closed: life, cancel: cancel}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == nil || c.state.Members[id].NodeID == "" || c.state.Members[id].Removed || c.transport.Core == nil {
		return nil, notFound()
	}
	sub, err := c.transport.Core.Subscribe(ctx, id)
	if err != nil {
		return nil, err
	}
	return &membershipTelemetry{Subscription: sub, cluster: c, host: id}, nil
}

// Both local and remote SSE carry current membership. A conflicting/replaced
// boot stream cannot bypass the inventory's acceptance checks through live SSE.
type membershipTelemetry struct {
	engine.Subscription[domain.HostTelemetry]
	cluster *ClusterNode
	host    domain.ID
}

func (s *membershipTelemetry) Next(ctx context.Context) (domain.HostTelemetry, error) {
	for {
		sample, err := s.Subscription.Next(ctx)
		if err != nil {
			return sample, err
		}
		c := s.cluster
		c.mu.Lock()
		local := s.host == c.keys.Identity.NodeID
		if local {
			sample.MembershipEpoch = c.keys.Identity.MembershipEpoch
		}
		accepted := local || (sample.MembershipEpoch != nil && c.state != nil && !c.state.Members[s.host].Removed && c.state.Members[s.host].Epoch == *sample.MembershipEpoch)
		c.mu.Unlock()
		if accepted && (local || c.inventory.accepts(sample)) {
			return sample, nil
		}
	}
}

func (s *Service) readHosts(ctx context.Context, request ReadRequest) (json.RawMessage, error) {
	hosts := s.Cluster.Hosts()
	if request.ID == nil {
		page, err := hostPage(hosts, request.Page)
		return marshalResult(page, err)
	}
	for _, host := range hosts {
		if host.ID != *request.ID {
			continue
		}
		if request.Subresource == "telemetry" {
			if host.ID == s.Identity.NodeID {
				sample := s.Sampler.Snapshot(ctx)
				sample.MembershipEpoch = host.MembershipEpoch
				return json.Marshal(sample)
			}
			sample, err := s.Cluster.Sample(host.ID)
			return marshalResult(sample, err)
		}
		return json.Marshal(host)
	}
	return nil, notFound()
}

type hostFilter struct {
	membership   domain.Membership
	reachability domain.Reachability
}

func parseHostFilter(values map[string]string) (hostFilter, error) {
	membership, err := enumFilter(values["membership"], domain.Unjoined, domain.Pending, domain.Full, domain.Removed)
	if err != nil {
		return hostFilter{}, err
	}
	reachability, err := enumFilter(values["reachability"], domain.Online, domain.ReachabilityStale, domain.Offline)
	return hostFilter{membership, reachability}, err
}

// Host filters and ordering stay typed; pagination only sees the final snapshot.
func hostPage(hosts []domain.Host, page domain.PageRequest) (domain.Page[domain.Host], error) {
	filter, err := parseHostFilter(page.Filters)
	if err != nil {
		return domain.Page[domain.Host]{}, err
	}
	items := slices.DeleteFunc(slices.Clone(hosts), func(h domain.Host) bool {
		return (filter.membership != "" && h.Membership != filter.membership) || (filter.reachability != "" && h.Reachability != filter.reachability)
	})
	slices.SortFunc(items, func(a, b domain.Host) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	return pageItems(items, page, "hosts", func(h domain.Host) []string { return []string{h.Name, string(h.ID)} })
}

func notFound() error { return &domain.Error{Code: "NOT_FOUND", Message: "Resource not found"} }
