package service

import (
	"context"
	"encoding/json"

	"nixmox/internal/domain"
	"nixmox/internal/engine/identity"
)

type credentialLocalRequest struct {
	CommandID    domain.ID                `json:"command_id"`
	Import       *domain.CredentialImport `json:"import,omitempty"`
	CredentialID domain.ID                `json:"credential_id,omitempty"`
	Repository   *domain.Repository       `json:"repository,omitempty"`
}

// secret queries never return a value: only public inventory, checks and effects.
func (c *ClusterNode) credentialLocal(ctx context.Context, kind string, p credentialLocalRequest) (any, error) {
	c.mu.Lock()
	eligible := c.keys.Trust != nil && c.forced()
	c.mu.Unlock()
	if !eligible {
		return nil, credentialMissing()
	}
	switch kind {
	case "credential.inventory":
		return c.localCredentials(ctx)
	case "credential.consume":
		if p.Import == nil {
			return nil, invalid("Missing credential import")
		}
		return c.consumeCredential(ctx, p.CommandID, *p.Import)
	case "credential.remove", "credential.removable":
		items, err := c.localCredentials(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.ID != p.CredentialID {
				continue
			}
			if item.Replicas[0].Source == "runtime_file" {
				return nil, invalid("Credential is owned by runtime configuration")
			}
			for _, ref := range []*string{c.config.Cache.SigningCredentialRef, c.config.Prepare.SOPS.CredentialRef} {
				if ref != nil && (*ref == item.Name || *ref == string(item.ID)) {
					return nil, invalid("Credential has a local cache or SOPS binding")
				}
			}
			for _, repository := range c.config.Repositories {
				if ref := repository.CredentialRef; ref != nil && (*ref == item.Name || *ref == string(item.ID)) {
					return nil, invalid("Credential has a declared repository binding")
				}
			}
		}
		if kind == "credential.removable" {
			return domain.Empty{}, nil
		}
		return domain.Empty{}, c.secrets.Remove(ctx, p.CredentialID)
	case "credential.check":
		value, kind, err := c.credentialValue(ctx, p.CredentialID)
		if err != nil {
			return nil, err
		}
		defer clear(value)
		if err = identity.CheckCredential(kind, value); err != nil {
			return nil, err
		}
		if kind == "git-http-token" || kind == "git-ssh-key" {
			if p.Repository == nil {
				return nil, invalid("Git credential checks require a repository")
			}
			p.Repository.Remote.CredentialRef = &p.CredentialID
			if err = c.git.CheckRead(ctx, *p.Repository); err != nil {
				return nil, err
			}
		} else if p.Repository != nil {
			return nil, invalid("Only Git credentials accept a repository")
		}
		return domain.CredentialCheck{Kind: kind, Checks: []domain.Check{{Name: "credential_access", Passed: true}}, Scope: domain.CredentialScope{RepositoryIDs: []domain.ID{}}, Passed: true}, nil
	}
	return nil, domain.Unavailable(kind)
}

func (c *ClusterNode) credentialCall(ctx context.Context, holder domain.ID, kind string, input credentialLocalRequest, result any) error {
	if holder == c.keys.Identity.NodeID {
		value, err := c.credentialLocal(ctx, kind, input)
		if err != nil {
			return err
		}
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, result)
	}
	return c.callResult(ctx, holder, kind, input, false, result)
}

// Inventory reconciliation is requested by membership/connection events, never a ticker.
func (s *Service) syncCredentials(ctx context.Context) {
	c := s.Cluster
	c.mu.Lock()
	authority := c.creator() && c.transport.Store != nil
	c.mu.Unlock()
	if !authority {
		return
	}
	for _, host := range c.Hosts() {
		if host.Membership != domain.Full || host.Reachability != domain.Online {
			continue
		}
		var items []domain.Credential
		if c.credentialCall(ctx, host.ID, "credential.inventory", credentialLocalRequest{}, &items) != nil {
			continue
		}
		for _, item := range items {
			if len(item.Replicas) == 1 && item.Replicas[0].NodeID == host.ID && item.ID == c.credentialID(item.Name) {
				_ = c.registerCredential(ctx, item)
			}
		}
	}
}
