package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

func credentialOperation(kind string) bool {
	return kind == "credential.import" || kind == "credential.check" || kind == "credential.remove"
}

func (c *ClusterNode) credentialInput(ctx context.Context, command domain.Command, value any) (credentialRecord, error) {
	if err := c.quorum(ctx); err != nil {
		return credentialRecord{}, err
	}
	var id domain.ID
	switch p := value.(type) {
	case domain.CredentialImport:
		if !credentialNameValid(p.Name) || p.StagingRef == "" {
			return credentialRecord{}, invalid("Credential name and staging reference required")
		}
		member, ok := c.state.Members[p.StorageNodeID]
		if !ok || member.Removed || !member.Full {
			return credentialRecord{}, invalid("Credential holder must be a full member")
		}
		id = sourceID("cred", string(c.state.Cluster.ID), p.Name)
	case domain.CredentialCheckPayload:
		id = p.CredentialID
	case domain.CredentialTarget:
		id = p.CredentialID
	default:
		return credentialRecord{}, invalid("Invalid credential command")
	}
	record, _, err := c.credentialLocked(ctx, id)
	var detail *domain.Error
	if command.Type == "credential.import" && errors.As(err, &detail) && detail.Code == "NOT_FOUND" {
		record = credentialRecord{Applied: map[domain.ID]bool{}}
		err = nil
	}
	if err != nil {
		return record, err
	}
	if record.Applied[command.ID] {
		return record, nil
	}
	if record.Removing != "" && record.Removing != command.ID {
		return record, invalid("Credential removal is in progress")
	}
	switch p := value.(type) {
	case domain.CredentialImport:
		if len(command.ExpectedVersions) != 0 {
			return record, invalid("Import has no edit version precondition")
		}
		if record.Removed {
			return record, invalid("Deleted credential names cannot be reused; import under a new name")
		}
		if record.Credential.ID != "" && record.Credential.Kind != p.Kind {
			return record, invalid("Credential kind conflicts with existing replicas")
		}
	case domain.CredentialTarget:
		if record.Removed {
			return record, notFound()
		}
		if record.Removing != command.ID && (len(command.ExpectedVersions) != 1 || command.ExpectedVersions["credential:"+string(id)] != record.Credential.Version) {
			return record, &domain.Error{Code: "VERSION_CONFLICT", Message: "Review the current credential version"}
		}
		for _, replica := range record.Credential.Replicas {
			if replica.Source == "runtime_file" {
				return record, invalid("Declared credentials cannot be removed")
			}
		}
		repositories, err := listRecords[domain.RepositoryState](ctx, c.transport.Store, "repository.*")
		if err != nil {
			return record, err
		}
		for _, repository := range repositories {
			ref := repository.Repository.Remote.CredentialRef
			if ref != nil && (*ref == id || string(*ref) == record.Credential.Name) {
				return record, invalid("Credential is referenced by a repository")
			}
		}
		for _, ref := range []*string{c.config.Cache.SigningCredentialRef, c.config.Prepare.SOPS.CredentialRef} {
			if ref != nil && (*ref == string(id) || *ref == record.Credential.Name) {
				return record, invalid("Credential is referenced by cache or SOPS settings")
			}
		}
	case domain.CredentialCheckPayload:
		if record.Removed || len(command.ExpectedVersions) != 0 {
			return record, invalid("Credential unavailable or unexpected version precondition")
		}
		git := strings.HasPrefix(record.Credential.Kind, "git-")
		if git != (p.RepositoryID != nil) {
			return record, invalid("Git checks require a repository; other checks do not accept one")
		}
		if p.RepositoryID != nil {
			if _, _, err := c.repositoryLocked(ctx, *p.RepositoryID); err != nil {
				return record, err
			}
		}
	}
	return record, nil
}

func (c *ClusterNode) credentialDefinitions(journal engine.LocalStore) []engine.TaskDefinition {
	var result []engine.TaskDefinition
	for _, kind := range []string{"credential.import", "credential.check", "credential.remove"} {
		run := func(ctx context.Context, e engine.Execution) error {
			admission, err := journal.Receipt(ctx, e.Task().CommandID)
			if err != nil {
				return err
			}
			if kind == "credential.check" {
				var input credentialLocalRequest
				if err := json.Unmarshal(admission.Envelope.Resolved, &input); err != nil {
					return err
				}
				value, err := c.credentialLocal(ctx, kind, input)
				if err != nil {
					return observeFailure(ctx, e, "credential", err)
				}
				return completePhase(ctx, e, "credential", value)
			}
			_, value, err := Catalog().Decode(admission.Envelope.Command)
			if err != nil {
				return err
			}
			c.mu.Lock()
			record, err := c.credentialInput(ctx, admission.Envelope.Command, value)
			c.mu.Unlock()
			if err != nil {
				return observeFailure(ctx, e, "credential", err)
			}
			if record.Applied[e.Task().CommandID] {
				return completePhase(ctx, e, "credential", record.Credential)
			}
			if kind == "credential.import" {
				p := value.(domain.CredentialImport)
				var item domain.Credential
				if err := c.credentialCall(ctx, p.StorageNodeID, "credential.consume", credentialLocalRequest{CommandID: e.Task().CommandID, Import: &p}, &item); err != nil {
					return err
				}
				if err := c.registerCredential(ctx, item); err != nil {
					return err
				}
				record.Credential.ID = item.ID
			} else {
				for _, replica := range record.Credential.Replicas {
					var result domain.Empty
					if err := c.credentialCall(ctx, replica.NodeID, "credential.removable", credentialLocalRequest{CredentialID: record.Credential.ID}, &result); err != nil {
						var detail *domain.Error
						if errors.As(err, &detail) && detail.Code == "INVALID_INPUT" && record.Removing == "" {
							return observeFailure(ctx, e, "credential", err)
						}
						return err
					}
				}
				// Fence new uses before deleting any replica. This marker survives an
				// unavailable holder or uncertain deletion acknowledgement.
				c.mu.Lock()
				_, inputErr := c.credentialInput(ctx, admission.Envelope.Command, value)
				if inputErr != nil {
					c.mu.Unlock()
					return observeFailure(ctx, e, "credential", inputErr)
				}
				current, revision, readErr := c.credentialLocked(ctx, record.Credential.ID)
				if readErr == nil {
					current.Removing = e.Task().CommandID
					_, readErr = writeRecord(ctx, c.transport.Store, recordKey("credential", current.Credential.ID), revision, current)
				}
				c.mu.Unlock()
				if readErr != nil {
					return readErr
				}
				for _, replica := range record.Credential.Replicas {
					var result domain.Empty
					if err := c.credentialCall(ctx, replica.NodeID, "credential.remove", credentialLocalRequest{CommandID: e.Task().CommandID, CredentialID: record.Credential.ID}, &result); err != nil {
						return err
					}
				}
			}
			c.mu.Lock()
			current, revision, err := c.credentialLocked(ctx, record.Credential.ID)
			if err == nil {
				if current.Applied == nil {
					current.Applied = map[domain.ID]bool{}
				}
				current.Applied[e.Task().CommandID] = true
				if kind == "credential.remove" {
					current.Removed = true
					current.Removing = ""
					current.Credential.Replicas = []domain.CredentialReplica{}
					current.Credential.Version++
				}
				_, err = writeRecord(ctx, c.transport.Store, recordKey("credential", current.Credential.ID), revision, current)
			}
			c.mu.Unlock()
			if err != nil {
				return err
			}
			return completePhase(ctx, e, "credential", current.Credential)
		}
		result = append(result, engine.TaskDefinition{Kind: kind, Phases: []engine.Phase{{Name: "credential", Interruptible: false, Resources: []engine.Resource{{Key: "credentials", Capacity: 1}, {Key: "repositories", Capacity: 1}}, Run: run, Reconcile: run}}})
	}
	return result
}

func (c *ClusterNode) readCredentials(ctx context.Context, request ReadRequest) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport.Store == nil {
		return nil, credentialMissing()
	}
	records, err := listRecords[credentialRecord](ctx, c.transport.Store, "credential.*")
	if err != nil {
		return nil, err
	}
	repositories, err := listRecords[domain.RepositoryState](ctx, c.transport.Store, "repository.*")
	if err != nil {
		return nil, err
	}
	var items []domain.Credential
	for _, record := range records {
		if record.Removed {
			continue
		}
		item := record.Credential
		item.Scope.RepositoryIDs = []domain.ID{}
		for _, repository := range repositories {
			ref := repository.Repository.Remote.CredentialRef
			if ref != nil && (*ref == item.ID || string(*ref) == item.Name) {
				item.Scope.RepositoryIDs = append(item.Scope.RepositoryIDs, repository.Repository.ID)
			}
		}
		if request.ID != nil && *request.ID == item.ID {
			return json.Marshal(item)
		}
		if kind := request.Page.Filters["kind"]; kind == "" || kind == item.Kind {
			items = append(items, item)
		}
	}
	if request.ID != nil {
		return nil, notFound()
	}
	if items == nil {
		items = []domain.Credential{}
	}
	slices.SortFunc(items, func(a, b domain.Credential) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	page, err := pageItems(items, request.Page, "credentials", func(item domain.Credential) []string { return []string{item.Name, string(item.ID)} })
	if err != nil {
		return nil, err
	}
	return json.Marshal(page)
}
