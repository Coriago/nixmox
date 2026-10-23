package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine/identity"
)

func (c *ClusterNode) completeMembership(ctx context.Context) error {
	state := *c.state
	state.Members = maps.Clone(state.Members)
	changed := state.Removals == nil
	if changed {
		state.Removals = map[domain.ID]domain.SignedRemoval{}
	}
	for id, member := range state.Members {
		if member.Version == 0 {
			member.Version = 1
			changed = true
		}
		if member.BrokerCertificate == "" {
			if id == state.Cluster.IssuerNodeID {
				member.BrokerCertificate = c.keys.BrokerCertificate()
			} else {
				request, ok := state.Requests[member.RequestID]
				if !ok {
					return errors.New("membership issuance request missing")
				}
				issued, err := c.keys.Issued(submission(request))
				if err != nil {
					return err
				}
				pin, err := identity.CertificateFingerprint(issued.BrokerCertificate)
				if err != nil {
					return err
				}
				member.BrokerCertificate = pin
			}
			changed = true
		}
		state.Members[id] = member
	}
	if changed {
		return c.saveState(ctx, state)
	}
	return nil
}
func (c *ClusterNode) syncMembershipContext() {
	var cluster domain.ID
	var epoch uint64
	if trust := c.keys.Trust; trust != nil {
		cluster, epoch = trust.Cluster.ID, trust.Issuance.MembershipEpoch
	}
	if c.scope != nil && c.scopeCluster == cluster && c.scopeEpoch == epoch {
		return
	}
	if c.cancelScope != nil {
		c.cancelScope()
	}
	c.scope, c.cancelScope = context.WithCancel(c.life)
	c.scopeCluster, c.scopeEpoch = cluster, epoch
}
func (c *ClusterNode) taskContext(current domain.Task) (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current.ClusterID == nil || *current.ClusterID != c.scopeCluster || current.MembershipEpoch != c.scopeEpoch || c.scopeEpoch == 0 {
		return nil, &domain.Error{Code: "MEMBERSHIP_REQUIRED", Message: "Task belongs to an inactive membership epoch"}
	}
	return c.scope, nil
}

func removalKey(node domain.ID, epoch uint64) domain.ID {
	return domain.ID(fmt.Sprintf("%s:%d", node, epoch))
}
func (c *ClusterNode) validateDeparture(command domain.Command, host domain.ID, epoch uint64, force bool, confirm string) error {
	member, ok := c.state.Members[host]
	if !ok || member.Removed || host != command.TargetID {
		return invalid("Host is not an active member")
	}
	if member.Full {
		return invalid("Full-member removal requires explicit replica migration, which is not implemented yet")
	}
	if c.state.Power != nil && c.state.Power.HostID == host {
		return invalid("Host has an unresolved power transition")
	}
	if host == c.state.Cluster.IssuerNodeID {
		return invalid("The sole issuer cannot leave or be removed")
	}
	if epoch != member.Epoch || len(command.ExpectedVersions) != 1 || command.ExpectedVersions["host:"+string(host)] != member.Version {
		return &domain.Error{Code: "VERSION_CONFLICT", Message: "Review the current host membership epoch and version"}
	}
	if command.Type == "host.remove" {
		name := c.inventory.name(host)
		if !force || (confirm != string(host) && (name == "" || confirm != name)) {
			return invalid("Force removal requires --force and the exact host name or ID")
		}
	}
	return nil
}

func (c *ClusterNode) depart(ctx context.Context, command domain.Command) (any, error) {
	var host domain.ID
	var epoch uint64
	var force bool
	var confirm string
	if command.Type == "cluster.leave" {
		var p domain.ClusterLeave
		if err := json.Unmarshal(command.Payload, &p); err != nil {
			return nil, err
		}
		host, epoch = p.HostID, p.MembershipEpoch
	} else {
		var p domain.HostRemove
		if err := json.Unmarshal(command.Payload, &p); err != nil {
			return nil, err
		}
		host, epoch, force, confirm = p.HostID, p.MembershipEpoch, p.Force, p.ConfirmHost
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return nil, err
	}
	notice, committed := c.state.Removals[removalKey(host, epoch)]
	if !committed {
		if err := c.validateDeparture(command, host, epoch, force, confirm); err != nil {
			return nil, err
		}
		// Hold admission's membership lock across drain inspection and commit.
		// No new command can be published into this epoch in between.
		if !force {
			if _, err := fetchQuery(ctx, c.transport, c.keys.Identity, host, "membership.prepare_leave", []byte("{}"), false); err != nil {
				return nil, err
			}
		}
		member := c.state.Members[host]
		member.Removed = true
		member.Version++
		var err error
		notice, err = c.keys.SignRemoval(domain.Removal{ClusterID: c.state.Cluster.ID, NodeID: host, MembershipEpoch: epoch, Status: "removed", RemovalVersion: member.Version})
		if err != nil {
			return nil, err
		}
		state := *c.state
		state.Members = maps.Clone(state.Members)
		state.Removals = maps.Clone(state.Removals)
		state.Members[host], state.Removals[removalKey(host, epoch)] = member, notice
		if err = c.saveState(ctx, state); err != nil {
			return nil, err
		}
	}
	// Reconciliation applies only the latest committed grants. It cannot remove
	// a later rejoin by replaying an old notice or command.
	if err := c.configure(ctx); err != nil {
		return nil, err
	}
	return struct {
		Removal       domain.SignedRemoval `json:"removal"`
		GrantsApplied bool                 `json:"grants_applied"`
		TargetCleanup string               `json:"target_cleanup"`
	}{notice, true, "pending_signed_notice"}, nil
}
func (c *ClusterNode) Removal(node domain.ID, epoch uint64) (domain.SignedRemoval, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == nil {
		return domain.SignedRemoval{}, notFound()
	}
	notice, ok := c.state.Removals[removalKey(node, epoch)]
	if !ok {
		return notice, notFound()
	}
	return notice, nil
}

// Signed public HTTP notices remain available after NATS grants are revoked.
// Failed HTTP/TLS connections alone never cause a node to clear membership.
func (c *ClusterNode) ReconcileMembership(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		c.mu.Lock()
		trust := c.keys.Trust
		if trust == nil || c.creator() {
			c.mu.Unlock()
			continue
		}
		address := trust.AuthorityAddress
		if address == "" && len(trust.Cluster.GatewayEndpoints) > 0 {
			address = trust.Cluster.GatewayEndpoints[len(trust.Cluster.GatewayEndpoints)-1].Address
		}
		node, epoch := c.keys.Identity.NodeID, trust.Issuance.MembershipEpoch
		c.mu.Unlock()
		api, err := enrollmentClient(address)
		if err != nil {
			continue
		}
		call, cancel := context.WithTimeout(ctx, domain.ExportTimeout)
		notice, err := api.Removal(call, node, epoch)
		cancel()
		if err != nil {
			continue
		}
		c.mu.Lock()
		if c.keys.VerifyRemoval(notice) == nil {
			if c.cancelScope != nil {
				c.cancelScope()
			}
			c.transport.Close()
			if c.keys.Retire(notice) == nil {
				_ = c.configure(ctx)
			}
		}
		c.mu.Unlock()
	}
}
