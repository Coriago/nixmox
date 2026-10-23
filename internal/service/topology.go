package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"path/filepath"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine/comms"
	"nixmox/internal/engine/identity"
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
func (c *ClusterNode) routeOptions(opts *comms.BrokerOptions) error {
	if c.keys.Trust.Topology == nil {
		return nil
	}
	topology := c.keys.Trust.Topology.Topology
	// NATS cannot elect a metadata leader from a self-route alone. Keep the
	// provisional store standalone until an explicit store migration exists.
	if len(topology.Peers) == 1 {
		return nil
	}
	local, ok := topology.Peers[c.keys.Identity.NodeID]
	if !ok {
		return nil
	}
	if local.Address != c.routeAddress() {
		return errors.New("configured route listener differs from approved topology")
	}
	opts.Pending = false
	opts.DataDir = filepath.Join(c.config.DataDir, "broker", "routed")
	opts.RouteAddress = local.Address
	var err error
	opts.RouteTLS, err = c.keys.RouteTLS()
	if err != nil {
		return err
	}
	for _, peer := range topology.Peers {
		key, pin, err := identity.CertificatePins(peer.Certificate)
		if err != nil {
			return err
		}
		opts.BrokerKeys = append(opts.BrokerKeys, key)
		opts.RouteCertificates = append(opts.RouteCertificates, pin)
		route, err := url.Parse("nats-route://" + peer.Address)
		if err != nil {
			return err
		}
		opts.Routes = append(opts.Routes, route)
	}
	return nil
}
func (c *ClusterNode) forced() bool {
	return c.keys.Trust != nil && c.keys.Trust.Topology != nil && c.keys.Trust.Topology.Topology.Peers[c.keys.Identity.NodeID].ForcedBy != ""
}
func (c *ClusterNode) validatePromotion(ctx context.Context, command domain.Command, p domain.ForceFullMembership) error {
	if err := c.quorum(ctx); err != nil {
		return err
	}
	if c.state.Topology == nil {
		return invalid("Legacy standalone store requires a supported backup/restore migration; it cannot be converted in place")
	}
	if len(c.state.Topology.Topology.Peers) == 1 {
		return invalid("Forced promotion requires standalone-to-cluster store migration, which is not implemented; membership is unchanged")
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
	if c.state.Members[p.HostID].Full {
		return invalid("Host is already a full member")
	}
	if p.HostID != c.host.ID && !c.state.Members[c.host.ID].Full {
		return invalid("Force-promote the creator first")
	}
	if _, ok := c.state.Topology.Topology.Peers[p.HostID]; !ok && len(c.state.Topology.Topology.Peers) >= 3 {
		return invalid("This topology currently supports three full brokers")
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
	if member.ForcedBy != command.ID {
		if err := c.validatePromotion(ctx, command, p); err != nil {
			return err
		}
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
		topology := c.state.Topology.Topology
		topology.Peers = maps.Clone(topology.Peers)
		topology.Version++
		topology.Peers[p.HostID] = domain.RoutePeer{NodeID: p.HostID, Epoch: member.Epoch, Address: address, Certificate: cert, ForcedBy: command.ID}
		if len(topology.Peers) == 3 {
			topology.Replicas = 3
		}
		signed, err := c.keys.SignTopology(topology)
		if err != nil {
			return err
		}
		member.Full, member.ForcedBy = true, command.ID
		member.Version++
		state := *c.state
		state.Members = maps.Clone(state.Members)
		state.Members[p.HostID] = member
		state.Topology = &signed
		if err = c.saveState(ctx, state); err != nil {
			return err
		}
	}
	if c.keys.Trust.Topology.Topology.Version < c.state.Topology.Topology.Version {
		if err := c.keys.ApplyTopology(*c.state.Topology); err != nil {
			return err
		}
		return c.configure(ctx)
	}
	return nil
}
func (c *ClusterNode) promote(ctx context.Context, command domain.Command) (any, error) {
	if err := c.commitPromotion(ctx, command); err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
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
				return map[string]any{"membership": "full", "deployment_verified": false, "forced_by": command.ID, "replicas": topology.Replicas}, nil
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
		c.mu.Lock()
		joined := c.keys.Trust != nil
		creator := c.creator()
		c.mu.Unlock()
		if !joined || creator {
			continue
		}
		data, err := c.call(ctx, "", "membership.topology", domain.Empty{}, true)
		if err != nil {
			continue
		}
		var next *domain.SignedTopology
		if json.Unmarshal(data, &next) != nil || next == nil {
			continue
		}
		c.mu.Lock()
		if c.keys.Trust != nil && (c.keys.Trust.Topology == nil || c.keys.Trust.Topology.Topology.Version < next.Topology.Version) {
			if err = c.keys.ApplyTopology(*next); err == nil {
				err = c.configure(ctx)
			}
		}
		c.mu.Unlock()
		if err != nil {
			return err
		}
	}
}
