package service

import (
	"sort"
	"sync"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine/telemetry"
)

type presence struct {
	topologyVersion domain.Version
	host            domain.Host
	stream          domain.ID
	epoch           uint64
	seen            time.Time
	conflict        bool
	sample          telemetry.SampleTracker
}

// inventory contains observations only. Callers supply committed membership;
// receiving a heartbeat or sample cannot create, renew or promote a member.
type inventory struct {
	mu           sync.Mutex
	observations map[domain.ID]*presence
}

func newInventory() *inventory { return &inventory{observations: map[domain.ID]*presence{}} }
func (i *inventory) heartbeat(h domain.Heartbeat, member domain.Member, cluster, issuer domain.ID) (bool, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	current := i.observations[h.Host.ID]
	if current == nil {
		current = &presence{}
		i.observations[h.Host.ID] = current
	}
	changed := current.epoch != member.Epoch || current.stream != h.StreamID || (current.host.Membership == domain.Full) != member.Full
	if current.epoch != member.Epoch || current.stream != h.StreamID {
		if current.epoch == member.Epoch && current.stream != "" && time.Since(current.seen) < domain.HeartbeatOffline {
			current.conflict = true
			return false, false
		}
		current.stream, current.epoch = h.StreamID, member.Epoch
		current.conflict = false
		current.sample.Reset(member.Epoch, h.StreamID)
	}
	current.seen = time.Now()
	current.topologyVersion = h.TopologyVersion
	host := h.Host
	if host.Cache != nil {
		cache := *host.Cache
		observed := current.seen
		cache.NodeID, cache.Endpoints, cache.ObservedAt = host.ID, host.Endpoints, &observed
		if !member.Full && host.ID != issuer {
			cache.Available = false
			cache.PublicKey = nil
		}
		host.Cache = &cache
	}
	host.Version, host.ClusterID, host.Membership = member.Version, &cluster, domain.Pending
	host.DeploymentVerified = false
	host.Capabilities = domain.Capabilities{Power: h.Host.Capabilities.Power}
	host.PendingReasons = []string{"configuration_not_deployed"}
	host.BrokerRole = "leaf"
	if host.ID == issuer {
		host.BrokerRole = "provisional"
	}
	if member.Full {
		host.Membership = domain.Full
		host.BrokerRole = "route"
		host.PendingReasons = []string{"forced_without_deployment_verification"}
	}
	current.host = host
	return true, changed
}
func (i *inventory) sample(sample domain.HostTelemetry) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if current := i.observations[sample.HostID]; current != nil && !current.conflict {
		current.sample.Accept(sample)
	}
}
func (i *inventory) applied(id domain.ID, version domain.Version) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	p := i.observations[id]
	return p != nil && !p.conflict && time.Since(p.seen) < domain.HeartbeatStale && p.topologyVersion >= version
}
func (i *inventory) name(id domain.ID) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if current := i.observations[id]; current != nil {
		return current.host.Name
	}
	return ""
}
func (i *inventory) accepts(sample domain.HostTelemetry) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	current := i.observations[sample.HostID]
	return current != nil && !current.conflict && current.stream == sample.StreamID && sample.MembershipEpoch != nil && current.epoch == *sample.MembershipEpoch
}
func (i *inventory) current(id domain.ID, epoch uint64) domain.HostTelemetry {
	i.mu.Lock()
	defer i.mu.Unlock()
	if current := i.observations[id]; current != nil && !current.conflict && current.epoch == epoch {
		if sample := current.sample.Current(time.Now()); sample != nil {
			return *sample
		}
	}
	return domain.HostTelemetry{SchemaVersion: 1, HostID: id, MembershipEpoch: &epoch, Observed: domain.Observed{Freshness: domain.Unknown}}
}
func (i *inventory) hosts(cluster domain.ID, members map[domain.ID]domain.Member, local domain.Host) []domain.Host {
	i.mu.Lock()
	defer i.mu.Unlock()
	hosts := make([]domain.Host, 0, len(members))
	for id, member := range members {
		if id == local.ID {
			hosts = append(hosts, local)
			continue
		}
		host := domain.Host{Record: domain.Record{SchemaVersion: 1, ID: id, Version: member.Version}, ClusterID: &cluster, Membership: domain.Pending, MembershipEpoch: &member.Epoch, Environment: domain.Bootstrap, BrokerRole: "leaf", Reachability: domain.Offline,
			Health: domain.Health{Status: "unknown", Reasons: []domain.HealthReason{}}, PendingReasons: []string{"configuration_not_deployed"}}
		if current := i.observations[id]; current != nil && current.epoch == member.Epoch {
			host = current.host
			seen := current.seen.UTC()
			host.LastSeen = &seen
			host.Reachability = domain.Online
			if time.Since(current.seen) > domain.HeartbeatStale {
				host.Reachability = domain.ReachabilityStale
			}
			if time.Since(current.seen) > domain.HeartbeatOffline {
				host.Reachability = domain.Offline
			}
			if current.conflict {
				host.Health = domain.Health{Status: "unhealthy", Reasons: []domain.HealthReason{{Code: "identity_conflict", Resource: string(id), Message: "Unconfirmed concurrent boot stream change"}}}
			}
		}
		host.Version = member.Version
		host.Configuration = member.Configuration
		if member.Full {
			host.Membership = domain.Full
			host.BrokerRole = "route"
			host.PendingReasons = []string{"forced_without_deployment_verification"}
		}
		if member.Removed {
			host.Membership = domain.Removed
			host.Reachability = domain.Offline
			host.PendingReasons = nil
		}
		hosts = append(hosts, host)
	}
	sort.Slice(hosts, func(a, b int) bool { return hosts[a].ID < hosts[b].ID })
	return hosts
}
