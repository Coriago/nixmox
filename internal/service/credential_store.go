package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode"

	js "github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	gitengine "nixmox/internal/engine/git"
	"nixmox/internal/engine/identity"
)

// Only public metadata and command acceptance proofs are replicated.
type credentialRecord struct {
	Credential domain.Credential  `json:"credential"`
	Applied    map[domain.ID]bool `json:"applied"`
	Removing   domain.ID          `json:"removing,omitempty"`
	Removed    bool               `json:"removed"`
}

func (c *ClusterNode) credentialID(name string) domain.ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	cluster := domain.ID("")
	if c.keys.Trust != nil {
		cluster = c.keys.Trust.Cluster.ID
	}
	return sourceID("cred", string(cluster), name)
}

func (c *ClusterNode) localCredentials(ctx context.Context) ([]domain.Credential, error) {
	items, err := c.secrets.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	for name, declaration := range c.config.Credentials {
		id := c.credentialID(name)
		state := "available"
		value, err := identity.ReadCredentialFile(declaration.SecretFile)
		if err != nil || identity.CheckCredential(declaration.Kind, value) != nil {
			state = "unavailable"
		}
		clear(value)
		items = slices.DeleteFunc(items, func(item domain.Credential) bool { return item.ID == id })
		items = append(items, domain.Credential{Record: domain.Record{SchemaVersion: 1, ID: id, Version: 1}, Name: name, Kind: declaration.Kind, Scope: domain.CredentialScope{RepositoryIDs: []domain.ID{}}, Replicas: []domain.CredentialReplica{{NodeID: c.keys.Identity.NodeID, State: state, Source: "runtime_file"}}})
	}
	return items, nil
}

func (c *ClusterNode) credentialValue(ctx context.Context, id domain.ID) ([]byte, string, error) {
	items, err := c.localCredentials(ctx)
	if err != nil {
		return nil, "", credentialMissing()
	}
	for _, item := range items {
		if item.ID != id && item.Name != string(id) {
			continue
		}
		if configured, ok := c.config.Credentials[item.Name]; ok {
			value, err := identity.ReadCredentialFile(configured.SecretFile)
			return value, item.Kind, err
		}
		reader, err := c.secrets.Open(ctx, item.ID)
		if err != nil {
			return nil, item.Kind, credentialMissing()
		}
		defer reader.Close()
		value, err := identity.ReadCredential(reader)
		return value, item.Kind, err
	}
	return nil, "", credentialMissing()
}

// Caller holds c.mu; select an online full holder, never a pending installer.
func (c *ClusterNode) credentialHolderLocked(record credentialRecord) (domain.ID, error) {
	if record.Removed || record.Removing != "" {
		return "", credentialMissing()
	}
	hosts := c.inventory.hosts(c.state.Cluster.ID, c.state.Members, c.localHost())
	for _, replica := range record.Credential.Replicas {
		for _, host := range hosts {
			if host.ID == replica.NodeID && host.Membership == domain.Full && host.Reachability == domain.Online && replica.State == "available" {
				return replica.NodeID, nil
			}
		}
	}
	return "", credentialMissing()
}

func credentialMissing() error {
	return &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "No available credential holder; no secret was copied to this node"}
}

func (c *ClusterNode) gitAuthentication(ctx context.Context, repository domain.Repository) (gitengine.Authentication, error) {
	if repository.Remote.CredentialRef == nil {
		return gitengine.Authentication{}, credentialMissing()
	}
	value, kind, err := c.credentialValue(ctx, *repository.Remote.CredentialRef)
	if err != nil {
		return gitengine.Authentication{}, err
	}
	defer clear(value)
	auth, err := gitengine.ManagedAuthentication(repository.Remote, kind, value)
	if err != nil {
		return gitengine.Authentication{}, &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "Git authentication settings or credential are invalid"}
	}
	return auth, nil
}

func (c *ClusterNode) credentialLocked(ctx context.Context, id domain.ID) (credentialRecord, uint64, error) {
	if c.transport.Store == nil {
		return credentialRecord{}, 0, credentialMissing()
	}
	record, revision, err := readRecord[credentialRecord](ctx, c.transport.Store.KV, recordKey("credential", id))
	if errors.Is(err, js.ErrKeyNotFound) {
		err = notFound()
	}
	return record, revision, err
}

// registerCredential adds a declared/managed holder without retaining secret paths.
// Existing tombstones cannot be resurrected by stale availability observations.
func (c *ClusterNode) registerCredential(ctx context.Context, item domain.Credential) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return err
	}
	old, revision, err := c.credentialLocked(ctx, item.ID)
	var detail *domain.Error
	missing := errors.As(err, &detail) && detail.Code == "NOT_FOUND"
	if err != nil && !missing {
		return err
	}
	if !missing && (old.Removed || old.Removing != "") {
		return nil
	}
	if missing {
		old = credentialRecord{Credential: item, Applied: map[domain.ID]bool{}}
	} else {
		if old.Credential.Name != item.Name || old.Credential.Kind != item.Kind {
			return &domain.Error{Code: "CONFIGURATION_CONFLICT", Message: "Credential declarations disagree on kind or name"}
		}
		changed, availabilityChanged := false, false
		for _, replica := range item.Replicas {
			index := slices.IndexFunc(old.Credential.Replicas, func(r domain.CredentialReplica) bool { return r.NodeID == replica.NodeID })
			if index < 0 {
				old.Credential.Replicas = append(old.Credential.Replicas, replica)
				changed = true
			} else if old.Credential.Replicas[index].Source != replica.Source {
				old.Credential.Replicas[index] = replica
				changed = true
			} else if old.Credential.Replicas[index].State != replica.State {
				old.Credential.Replicas[index].State = replica.State
				availabilityChanged = true
			}
		}
		if !changed && !availabilityChanged {
			return nil
		}
		if changed {
			old.Credential.Version++
		}
	}
	_, err = writeRecord(ctx, c.transport.Store.KV, recordKey("credential", item.ID), revision, old)
	return err
}

func credentialNameValid(name string) bool {
	return name != "" && len(name) <= 128 && strings.TrimSpace(name) == name && !strings.ContainsFunc(name, unicode.IsControl)
}

// consumeCredential is holder-local and authorized only through the issuer's
// authenticated query. The ingress binding is rechecked before private effects.
func (c *ClusterNode) consumeCredential(ctx context.Context, command domain.ID, p domain.CredentialImport) (domain.Credential, error) {
	in, stage, err := c.secrets.Inspect(ctx, p.StagingRef)
	if err != nil {
		return domain.Credential{}, credentialMissing()
	}
	if stage.CommandID != command || in.Name != p.Name || in.Kind != p.Kind || in.HolderID != p.StorageNodeID || p.StorageNodeID != c.keys.Identity.NodeID {
		return domain.Credential{}, invalid("Staged credential does not match admitted import")
	}
	if _, declared := c.config.Credentials[p.Name]; declared {
		return domain.Credential{}, invalid("Credential is owned by runtime configuration")
	}
	c.mu.Lock()
	if c.keys.Trust == nil || !c.forced() {
		c.mu.Unlock()
		return domain.Credential{}, credentialMissing()
	}
	id := sourceID("cred", string(c.keys.Trust.Cluster.ID), p.Name)
	err = c.secrets.Consume(ctx, p.StagingRef, id)
	c.mu.Unlock()
	if err != nil {
		return domain.Credential{}, credentialMissing()
	}
	return domain.Credential{Record: domain.Record{SchemaVersion: 1, ID: id, Version: 1}, Name: p.Name, Kind: p.Kind, Scope: domain.CredentialScope{RepositoryIDs: []domain.ID{}}, Replicas: []domain.CredentialReplica{{NodeID: p.StorageNodeID, State: "available", Source: "managed"}}}, nil
}

func (s *Service) Import(ctx context.Context, in engine.SecretIngress, reader io.Reader) (domain.Receipt, error) {
	c := s.Cluster
	if c == nil {
		return domain.Receipt{}, credentialMissing()
	}
	c.mu.Lock()
	eligible := c.keys.Trust != nil && c.forced()
	transport, local := c.transport, c.keys.Identity.NodeID
	c.mu.Unlock()
	if !eligible {
		return domain.Receipt{}, invalid("Credential import requires full membership")
	}
	if !credentialNameValid(in.Name) || in.CommandID == "" || in.HolderID == "" {
		return domain.Receipt{}, invalid("Command ID, holder and printable credential name are required")
	}
	if in.HolderID != local {
		if transport.Core == nil {
			return domain.Receipt{}, credentialMissing()
		}
		return transport.Core.Import(ctx, in, reader)
	}
	if _, declared := c.config.Credentials[in.Name]; declared {
		return domain.Receipt{}, invalid("Credential is owned by runtime configuration")
	}
	value, err := identity.ReadCredential(reader)
	if err != nil {
		return domain.Receipt{}, err
	}
	defer clear(value)
	if err = identity.CheckCredential(in.Kind, value); err != nil {
		return domain.Receipt{}, err
	}
	c.mu.Lock()
	if c.keys.Trust == nil || !c.forced() {
		c.mu.Unlock()
		return domain.Receipt{}, credentialMissing()
	}
	if err := c.secrets.Expire(ctx); err != nil {
		c.mu.Unlock()
		return domain.Receipt{}, err
	}
	stage, err := c.secrets.Stage(ctx, in, bytes.NewReader(value))
	c.mu.Unlock()
	if err != nil {
		return domain.Receipt{}, err
	}
	payload, err := json.Marshal(domain.CredentialImport{Name: in.Name, Kind: in.Kind, StorageNodeID: in.HolderID, StagingRef: stage.Reference})
	if err != nil {
		return domain.Receipt{}, err
	}
	return s.Submit(ctx, domain.Command{ID: in.CommandID, Type: "credential.import", TargetID: local, Payload: payload})
}
