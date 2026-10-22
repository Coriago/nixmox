package credentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"nixmox/internal/domain"
	"nixmox/internal/service"
	"nixmox/internal/storage"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type importPayload struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	StorageNodeID string `json:"storage_node_id"`
	StagingRef    string `json:"staging_ref"`
}
type checkPayload struct {
	CredentialID string `json:"credential_id"`
	RepositoryID string `json:"repository_id,omitempty"`
}

func (v *Vault) Operations() []service.Operation {
	return []service.Operation{
		{Kind: "credential.import", Scope: "cluster", Timeout: 30 * time.Second, SafeRetry: true, Validate: func(c domain.Command) error {
			var p importPayload
			if domain.StrictJSON(c.Payload, &p) != nil || !domain.SafeID.MatchString(p.Name) || !validKind(p.Kind) || p.StorageNodeID != c.TargetID || !domain.SafeID.MatchString(p.StagingRef) || len(c.ExpectedVersions) != 0 {
				return domain.Err("INVALID_PAYLOAD", "Credential import accepts metadata and a secret staging reference only.", 422)
			}
			return nil
		}, Run: v.importCredential, Reconcile: v.importCredential},
		{Kind: "credential.check", Scope: "cluster", Timeout: 2 * time.Minute, SafeRetry: true, Validate: func(c domain.Command) error {
			var p checkPayload
			if domain.StrictJSON(c.Payload, &p) != nil || !domain.SafeID.MatchString(p.CredentialID) || p.RepositoryID != "" && !domain.SafeID.MatchString(p.RepositoryID) || len(c.ExpectedVersions) != 0 {
				return domain.Err("INVALID_PAYLOAD", "Credential check requires its ID and optional repository ID.", 422)
			}
			return nil
		}, Run: v.checkCredential, Reconcile: v.checkCredential},
		{Kind: "credential.remove", Scope: "cluster", Timeout: 30 * time.Second, SafeRetry: true, Validate: func(c domain.Command) error {
			var p struct {
				ID string `json:"credential_id"`
			}
			if domain.StrictJSON(c.Payload, &p) != nil || !domain.SafeID.MatchString(p.ID) || len(c.ExpectedVersions) != 1 || c.ExpectedVersions["credential:"+p.ID] < 1 {
				return domain.Err("INVALID_PAYLOAD", "Removal requires a reviewed credential version.", 422)
			}
			return nil
		}, Run: v.removeCredential, Reconcile: v.removeCredential},
	}
}
func emptyScope() domain.CredentialScope {
	return domain.CredentialScope{RepositoryIDs: []string{}, CacheNodeIDs: []string{}, UpdaterNodeIDs: []string{}}
}
func (v *Vault) publishReplica(ctx context.Context, name, kind, source string, available bool, public *string, importing bool) (domain.Credential, error) {
	id, node := ID(name), v.Identity().NodeID
	for tries := 0; tries < 4; tries++ {
		var c domain.Credential
		rev, err := v.State.Get(ctx, "credential."+id, &c)
		if err != nil && !missing(err) {
			return c, err
		}
		if missing(err) {
			c = domain.Credential{ID: id, Name: name, Kind: kind, Scope: emptyScope(), Replicas: []domain.CredentialReplica{}, State: "available"}
		}
		if c.State == "removed" {
			if !importing {
				return c, unavailable()
			}
			c.PublicKey, c.RemovalCommandID, c.Kind = nil, "", kind
		}
		if c.Name != name || c.Kind != kind {
			return c, domain.Err("CONFIGURATION_CONFLICT", "Credential declarations disagree about name or kind.", 409)
		}
		if c.PublicKey != nil && public != nil && *c.PublicKey != *public {
			return c, domain.Err("CONFIGURATION_CONFLICT", "Credential replicas have different public identities.", 409)
		}
		publicChanged := c.PublicKey == nil && public != nil
		if c.State == "removing" {
			return c, domain.Err("CREDENTIAL_UNAVAILABLE", "Credential removal is in progress.", 503)
		}
		if public != nil {
			c.PublicKey = public
		}
		c.State = "available"
		replica := domain.CredentialReplica{NodeID: node, Source: source, State: "unavailable"}
		if available {
			replica.State = "available"
		}
		found := false
		unchanged := false
		for i, r := range c.Replicas {
			if r.NodeID == node {
				found = true
				unchanged = r == replica
				c.Replicas[i] = replica
				break
			}
		}
		if !found {
			c.Replicas = append(c.Replicas, replica)
		}
		if unchanged && !publicChanged && rev != 0 {
			return c, nil
		}
		sort.Slice(c.Replicas, func(i, j int) bool { return c.Replicas[i].NodeID < c.Replicas[j].NodeID })
		c.Version++
		if _, err = v.State.CAS(ctx, "credential."+id, c, rev); err == nil {
			return c, nil
		} else {
			var conflict *domain.Error
			if !errorsAsConflict(err, &conflict) {
				return c, err
			}
		}
	}
	return domain.Credential{}, domain.Err("VERSION_CONFLICT", "Credential replicas changed during publication.", 409)
}
func errorsAsConflict(err error, out **domain.Error) bool {
	e, ok := err.(*domain.Error)
	if ok {
		*out = e
	}
	return ok && e.Code == "VERSION_CONFLICT"
}
func (v *Vault) importCredential(ctx context.Context, e *service.Execution) (any, error) {
	var p importPayload
	_ = json.Unmarshal(e.Command.Payload, &p)
	if err := v.own(ctx, e.Command.TargetID); err != nil {
		return nil, err
	}
	if _, declared := v.Declared[p.Name]; declared {
		return nil, domain.Err("DECLARED_RESOURCE", "A runtime-file credential cannot be overwritten.", 409)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	store := &storage.Store{Root: v.Root}
	var stage stageRecord
	if err := store.Read("staging/"+p.StagingRef+".json", &stage); err != nil {
		return nil, unavailable()
	}
	if stage.Request.ID != e.Command.ID || stage.Request.Name != p.Name || stage.Request.Kind != p.Kind || stage.Request.StorageNodeID != p.StorageNodeID {
		return nil, domain.Err("INVALID_PAYLOAD", "Staging reference does not match this command.", 422)
	}
	id := ID(p.Name)
	var local localRecord
	if err := store.Read("managed/"+id+".json", &local); err == nil && local.CommandID != e.Command.ID {
		return nil, domain.Err("CREDENTIAL_EXISTS", "Remove the managed credential before replacing its value.", 409)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, unavailable()
	}
	path := filepath.Join(v.Root, "staging", p.StagingRef+".secret")
	if local.CommandID == e.Command.ID {
		path = filepath.Join(v.Root, "managed", id+".secret")
	}
	value, err := readPrivate(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range value {
			value[i] = 0
		}
	}()
	sum := sha256.Sum256(value)
	if hex.EncodeToString(sum[:]) != stage.Digest {
		return nil, domain.Err("CREDENTIAL_INVALID", "Staged credential failed private integrity verification.", 409)
	}
	check, err := Validate(p.Kind, value)
	if err != nil {
		return nil, err
	}
	prior, priorErr := v.Credential(ctx, id)
	if priorErr != nil && !missing(priorErr) {
		return nil, priorErr
	}
	if priorErr == nil && prior.State != "removed" && (prior.State == "removing" || prior.Kind != p.Kind || prior.PublicKey != nil && check.PublicKey != nil && *prior.PublicKey != *check.PublicKey) {
		return nil, domain.Err("CONFIGURATION_CONFLICT", "Credential import conflicts with its active public identity.", 409)
	}
	if local.CommandID == "" {
		if err = store.WriteBytes("managed/"+id+".secret", value); err != nil {
			return nil, unavailable()
		}
		if err = store.Write("managed/"+id+".json", localRecord{Name: p.Name, Kind: p.Kind, CommandID: e.Command.ID}); err != nil {
			return nil, unavailable()
		}
	}
	credential, err := v.publishReplica(ctx, p.Name, p.Kind, "managed", true, check.PublicKey, true)
	if err != nil {
		return nil, err
	}
	stage.Claimed = true
	if err = store.Write("staging/"+p.StagingRef+".json", stage); err != nil {
		return nil, unavailable()
	}
	if err = os.Remove(filepath.Join(v.Root, "staging", p.StagingRef+".secret")); err != nil && !os.IsNotExist(err) {
		return nil, unavailable()
	}
	return credential, nil
}
func (v *Vault) checkCredential(ctx context.Context, e *service.Execution) (any, error) {
	var p checkPayload
	_ = json.Unmarshal(e.Command.Payload, &p)
	c, err := v.Credential(ctx, p.CredentialID)
	if err != nil {
		return nil, err
	}
	kind, value, err := v.Secret(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range value {
			value[i] = 0
		}
	}()
	result, err := Validate(kind, value)
	if err != nil {
		return nil, err
	}
	if kind == "git-http-token" || kind == "git-ssh-key" {
		if p.RepositoryID == "" {
			return nil, domain.Err("REPOSITORY_REQUIRED", "Git credential checks require a repository.", 422)
		}
		if v.GitCheck == nil {
			return nil, unavailable()
		}
		if err = v.GitCheck(ctx, c, p.RepositoryID); err != nil {
			return nil, err
		}
		result.Checks = append(result.Checks, "read_access")
		result.Scope = "repository_read"
	} else if p.RepositoryID != "" {
		return nil, domain.Err("INVALID_PAYLOAD", "This credential check has no repository scope.", 422)
	}
	return result, nil
}
func (v *Vault) removeCredential(ctx context.Context, e *service.Execution) (any, error) {
	var p struct {
		ID string `json:"credential_id"`
	}
	_ = json.Unmarshal(e.Command.Payload, &p)
	if err := v.own(ctx, e.Command.TargetID); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var c domain.Credential
	rev, err := v.State.Get(ctx, "credential."+p.ID, &c)
	if err != nil {
		return nil, err
	}
	if c.State == "removed" {
		return c, nil
	}
	if c.State != "removing" && c.Version != e.Command.ExpectedVersions["credential:"+c.ID] {
		return nil, domain.Err("VERSION_CONFLICT", "Credential changed after review.", 409)
	}
	if c.State == "removing" && c.RemovalCommandID != e.Command.ID {
		return nil, domain.Err("VERSION_CONFLICT", "Another credential removal is in progress.", 409)
	}
	if _, declared := v.Declared[c.Name]; declared {
		return nil, domain.Err("DECLARED_RESOURCE", "Declared credential sources cannot be removed through the console.", 409)
	}
	if len(c.Scope.RepositoryIDs)+len(c.Scope.CacheNodeIDs)+len(c.Scope.UpdaterNodeIDs) > 0 {
		return nil, domain.Err("RESOURCE_IN_USE", "Credential has active repository, cache or updater bindings.", 409)
	}
	if used, err := v.repositoryReferences(ctx, c); err != nil {
		return nil, err
	} else if used {
		return nil, domain.Err("RESOURCE_IN_USE", "Credential is referenced by a repository.", 409)
	}
	if len(c.Replicas) != 1 || c.Replicas[0].NodeID != v.Identity().NodeID {
		return nil, service.Block("deleting_replicas", "REPLICA_UNAVAILABLE", "Removal requires deletion receipts from every managed replica.")
	}
	if c.Replicas[0].Source != "managed" {
		return nil, domain.Err("DECLARED_RESOURCE", "Runtime credential source is read-only.", 409)
	}
	if c.State != "removing" {
		c.State = "removing"
		c.RemovalCommandID = e.Command.ID
		c.Version++
		rev, err = v.State.CAS(ctx, "credential."+c.ID, c, rev)
		if err != nil {
			return nil, err
		}
	}
	if err = e.Checkpoint("deleting_local_replica", map[string]string{"credential_id": c.ID}, false); err != nil {
		return nil, err
	}
	for _, suffix := range []string{".secret", ".json"} {
		if err = os.Remove(filepath.Join(v.Root, "managed", c.ID+suffix)); err != nil && !os.IsNotExist(err) {
			return nil, unavailable()
		}
	}
	c.State = "removed"
	c.Version++
	c.Replicas = []domain.CredentialReplica{}
	if _, err = v.State.CAS(ctx, "credential."+c.ID, c, rev); err != nil {
		return nil, err
	}
	return c, nil
}

// Reconcile publishes only public capabilities and rechecks runtime-file loss.
func (v *Vault) Reconcile(ctx context.Context) error {
	if err := v.own(ctx, v.Identity().NodeID); err != nil {
		return err
	}
	for name, c := range v.Declared {
		value, err := readPrivate(c.SecretFile)
		var public *string
		available := false
		if err == nil {
			check, err := Validate(c.Kind, value)
			if err == nil {
				public = check.PublicKey
				available = true
			}
			for i := range value {
				value[i] = 0
			}
		}
		if _, err = v.publishReplica(ctx, name, c.Kind, "runtime_file", available, public, false); err != nil {
			return err
		}
	}
	return nil
}
