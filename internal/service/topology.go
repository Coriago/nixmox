package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine/comms"
	"nixmox/internal/engine/identity"
	"nixmox/internal/engine/telemetry"
	"nixmox/internal/logging"
)

func (c *ClusterNode) routeAddress() string {
	host, _, _ := net.SplitHostPort(c.leafAddress())
	return net.JoinHostPort(host, fmt.Sprint(c.config.NATS.RoutePort))
}
func (c *ClusterNode) initialTopology(cluster domain.Cluster, r domain.EnrollmentSubmission, issued domain.Issuance) (domain.SignedTopology, error) {
	cert, err := c.keys.RouteCertificate(r.NodeID, issued.MembershipEpoch, r.BrokerCSR, c.keys.BrokerKey())
	if err != nil {
		return domain.SignedTopology{}, err
	}
	return c.keys.SignTopology(domain.Topology{ClusterID: cluster.ID, Version: 1, Replicas: 1, Peers: map[domain.ID]domain.RoutePeer{r.NodeID: {NodeID: r.NodeID, Epoch: issued.MembershipEpoch, Address: c.routeAddress(), Certificate: cert}}})
}
func (c *ClusterNode) routeOptions(opts *comms.BrokerOptions, signed *domain.SignedTopology) error {
	if signed == nil || len(signed.Topology.Peers) == 1 {
		return nil
	}
	topology := signed.Topology
	local, ok := topology.Peers[c.host.ID]
	if !ok {
		return nil
	}
	if local.Address != c.routeAddress() {
		return errors.New("configured route listener differs from approved topology")
	}
	opts.Pending = false
	opts.RouteAddress = local.Address
	var err error
	opts.RouteTLS, err = c.keys.RouteTLSFor(signed)
	if err != nil {
		return err
	}
	for id, peer := range topology.Peers {
		key, pin, err := identity.CertificatePins(peer.Certificate)
		if err != nil {
			return err
		}
		opts.BrokerKeys = append(opts.BrokerKeys, key)
		opts.RouteCertificates = append(opts.RouteCertificates, pin)
		if id == c.host.ID {
			continue
		}
		route, err := url.Parse("nats-route://" + peer.Address)
		if err != nil {
			return err
		}
		opts.Routes = append(opts.Routes, route)
	}
	return nil
}
func (c *ClusterNode) forced() bool {
	return c.keys.Trust != nil && c.keys.Trust.Topology != nil && c.keys.Trust.Topology.Topology.Peers[c.host.ID].ForcedBy != ""
}

// Signed public topology remains reachable while NATS changes generations.
// Recipients verify the pinned issuer, epoch, certificate and monotonic version.
func (c *ClusterNode) Topology() (*domain.SignedTopology, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys.Trust == nil || c.keys.Trust.Topology == nil {
		return nil, notFound()
	}
	return c.keys.Trust.Topology, nil
}
func (c *ClusterNode) validatePromotion(ctx context.Context, command domain.Command, p domain.ForceFullMembership) error {
	if err := c.quorum(ctx); err != nil {
		return err
	}
	if c.state.Topology == nil {
		return invalid("Cluster has no signed topology")
	}
	if c.state.Power != nil {
		return invalid("A power transition is unresolved")
	}
	name := c.inventory.name(p.HostID)
	if p.HostID == c.host.ID {
		name = c.host.Name
	}
	if err := c.validateHost(command, p.HostID, p.MembershipEpoch, p.ConfirmHost, name); err != nil {
		return err
	}
	member := c.state.Members[p.HostID]
	if command.Type == "host.demote" {
		if !member.Full {
			return invalid("Host is not a full member")
		}
		if p.HostID == c.host.ID {
			return invalid("The issuer must retain its durable broker")
		}
	} else {
		if member.Full {
			return invalid("Host is already a full member")
		}
		if p.HostID != c.host.ID && !c.state.Members[c.host.ID].Full {
			return invalid("Force-promote the creator first")
		}
		if _, ok := c.state.Topology.Topology.Peers[p.HostID]; !ok && len(c.state.Topology.Topology.Peers) >= 3 {
			return invalid("This topology currently supports three full brokers")
		}
	}
	return nil
}
func (c *ClusterNode) commitPromotion(ctx context.Context, command domain.Command) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var p domain.ForceFullMembership
	if err := json.Unmarshal(command.Payload, &p); err != nil {
		return err
	}
	member := c.state.Members[p.HostID]
	if member.TopologyCommand != command.ID {
		if err := c.validatePromotion(ctx, command, p); err != nil {
			return err
		}
		topology := c.state.Topology.Topology
		topology.Peers = maps.Clone(topology.Peers)
		topology.Version++
		if command.Type == "host.demote" {
			if err := c.transport.RemovePeer(ctx, p.HostID, len(topology.Peers)-1); err != nil {
				return err
			}
			delete(topology.Peers, p.HostID)
			member.Full, member.ForcedBy = false, ""
		} else {
			address := c.routeAddress()
			var request domain.EnrollmentSubmission
			var err error
			if p.HostID == c.host.ID {
				request, err = c.keys.Request(member.RequestID, c.state.Cluster.Name)
			} else {
				request = submission(c.state.Requests[member.RequestID])
				var data json.RawMessage
				data, err = fetchQuery(ctx, c.transport, c.keys.Identity, p.HostID, "membership.route", []byte("{}"), false)
				if err == nil {
					err = json.Unmarshal(data, &address)
				}
			}
			if err != nil {
				return err
			}
			cert, err := c.keys.RouteCertificate(p.HostID, member.Epoch, request.BrokerCSR, member.BrokerKey)
			if err != nil {
				return err
			}
			topology.Peers[p.HostID] = domain.RoutePeer{NodeID: p.HostID, Epoch: member.Epoch, Address: address, Certificate: cert, ForcedBy: command.ID}
			member.Full, member.ForcedBy = true, command.ID
		}
		topology.Replicas = 1
		if len(topology.Peers) == 3 {
			topology.Replicas = 3
		}
		signed, err := c.keys.SignTopology(topology)
		if err != nil {
			return err
		}
		member.Version++
		member.TopologyCommand = command.ID
		state := *c.state
		state.Members = maps.Clone(state.Members)
		state.Members[p.HostID] = member
		state.Topology = &signed
		if err = c.saveState(ctx, state); err != nil {
			return err
		}
	}
	return c.stageTopology(*c.state.Topology)
}
func (c *ClusterNode) stageTopology(next domain.SignedTopology) error {
	trust := *c.keys.Trust
	if trust.BrokerTopology == nil {
		trust.BrokerTopology = trust.Topology
	}
	trust.Topology = &next
	return c.keys.Install(trust)
}
func (c *ClusterNode) promote(ctx context.Context, command domain.Command) (any, error) {
	c.topologyMu.Lock()
	defer c.topologyMu.Unlock()
	wait, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := c.commitPromotion(wait, command); err != nil {
		return nil, err
	}
	if err := c.applyTopology(wait); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		c.mu.Lock()
		topology := c.state.Topology.Topology
		transport := c.transport
		ready := true
		for id := range topology.Peers {
			if id != c.host.ID && !c.inventory.applied(id, topology.Version) {
				ready = false
			}
		}
		c.mu.Unlock()
		if ready {
			last = nil
			if topology.Replicas == 3 {
				last = transport.Store.ExpandReplicas(wait)
			}
			if last == nil {
				last = transport.Store.ReplicasReady(wait, topology.Replicas)
			}
			if last == nil {
				membership := "full"
				if command.Type == "host.demote" {
					membership = "pending"
				}
				return map[string]any{"membership": membership, "deployment_verified": false, "replicas": topology.Replicas}, nil
			}
		}
		select {
		case <-wait.Done():
			return nil, errors.Join(wait.Err(), last)
		case <-ticker.C:
		}
	}
}
func (c *ClusterNode) ReconcileTopology(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		_ = c.reconcileTopology(ctx)
	}
}

func (c *ClusterNode) reconcileTopology(ctx context.Context) (err error) {
	c.mu.Lock()
	trust := c.keys.Trust
	creator := c.creator()
	c.mu.Unlock()
	if trust == nil {
		return nil
	}
	if !c.topologyMu.TryLock() {
		return nil
	}
	ctx, span := telemetry.Background(ctx, "topology.reconcile")
	defer func() { telemetry.End(span, err) }()
	var pollErr error
	if !creator {
		address := trust.AuthorityAddress
		if address == "" && len(trust.Cluster.GatewayEndpoints) > 0 {
			address = trust.Cluster.GatewayEndpoints[0].Address
		}
		api, e := enrollmentClient(address)
		if e == nil {
			call, cancel := context.WithTimeout(ctx, domain.ExportTimeout)
			next, e := api.Topology(call)
			cancel()
			if e == nil {
				c.mu.Lock()
				if c.keys.Trust != nil && (c.keys.Trust.Topology == nil || next.Topology.Version > c.keys.Trust.Topology.Topology.Version) {
					err = c.stageTopology(next)
				}
				c.mu.Unlock()
			}
			if e != nil {
				pollErr = e
			}
		} else {
			pollErr = e
		}
	}
	if err == nil {
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = c.applyTopology(call)
		cancel()
	}
	c.topologyMu.Unlock()
	// A failed poll does not prevent applying an already committed topology.
	err = errors.Join(err, pollErr)
	if err != nil {
		logging.From(ctx).Error("topology reconciliation", "error", err)
	}
	return err
}
