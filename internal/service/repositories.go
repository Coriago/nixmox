package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	js "github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	gitengine "nixmox/internal/engine/git"
)

func sourceID(kind string, parts ...string) domain.ID {
	return domain.ID(kind + "_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join(parts, "\x00"))).String())
}
func repositoryOperation(kind string) bool {
	return kind == "repository.import" || kind == "repository.fetch" || kind == "repository.remote.set"
}

// repositoryLocked is used with the membership mutex held: a store generation
// cannot be retired underneath a metadata read or acceptance CAS.
func (c *ClusterNode) repositoryLocked(ctx context.Context, id domain.ID) (domain.RepositoryState, engine.Revision, error) {
	if c.transport.Store == nil {
		return domain.RepositoryState{}, 0, domain.Unavailable("repository metadata")
	}
	state, revision, err := readRecord[domain.RepositoryState](ctx, c.transport.Store, recordKey("repository", id))
	if errors.Is(err, js.ErrKeyNotFound) {
		err = notFound()
	}
	return state, revision, err
}
func (c *ClusterNode) repository(ctx context.Context, id domain.ID) (domain.RepositoryState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, _, err := c.repositoryLocked(ctx, id)
	return state, err
}
func (c *ClusterNode) repositoryInput(ctx context.Context, command domain.Command, value any) (domain.RepositoryState, error) {
	// Caller owns c.mu. This validates again at execution, not only at admission.
	if err := c.quorum(ctx); err != nil {
		return domain.RepositoryState{}, err
	}
	var state domain.RepositoryState
	var id domain.ID
	switch p := value.(type) {
	case domain.RepositoryImport:
		if strings.TrimSpace(p.Name) != p.Name || p.Name == "" || len(p.Name) > 128 || strings.ContainsFunc(p.Name, unicode.IsControl) {
			return state, invalid("Repository name must contain 1–128 printable characters without surrounding whitespace")
		}
		if p.UpstreamBranch == "" {
			p.UpstreamBranch = "main"
		}
		id = sourceID("r", string(c.state.Cluster.ID), p.Name)
		state = domain.RepositoryState{Repository: domain.Repository{Record: domain.Record{SchemaVersion: 1, ID: id}, Name: p.Name, Remote: domain.Remote{URL: p.RemoteURL, CredentialRef: p.CredentialRef}, Upstream: domain.Upstream{Branch: p.UpstreamBranch}}, Refs: []domain.Reference{}, Applied: map[domain.ID]domain.RepositoryResult{}}
		old, _, err := c.repositoryLocked(ctx, id)
		if err == nil {
			if _, ok := old.Applied[command.ID]; ok {
				return old, nil
			}
			return state, &domain.Error{Code: "CONFIGURATION_CONFLICT", Message: "Repository alias is already registered"}
		}
		var detail *domain.Error
		if !errors.As(err, &detail) || detail.Code != "NOT_FOUND" {
			return state, err
		}
	case domain.RepositoryTarget:
		id = p.RepositoryID
	case domain.RepositoryRemoteSet:
		id = p.RepositoryID
	default:
		return state, domain.Unavailable(command.Type)
	}
	if id == "" {
		return state, invalid("Repository ID is required")
	}
	if command.Type != "repository.import" {
		var err error
		state, _, err = c.repositoryLocked(ctx, id)
		if err != nil {
			return state, err
		}
		if _, ok := state.Applied[command.ID]; ok {
			return state, nil
		}
	}
	expected := domain.Versions{}
	if p, ok := value.(domain.RepositoryRemoteSet); ok {
		expected["repository:"+string(id)] = state.Repository.Version
		state.Repository.Remote.URL = p.URL
		if p.UpstreamBranch != nil {
			state.Repository.Upstream.Branch = *p.UpstreamBranch
		}
	}
	if len(expected) != len(command.ExpectedVersions) {
		return state, invalid("Unexpected or missing version preconditions")
	}
	for key, version := range expected {
		if command.ExpectedVersions[key] != version {
			return state, &domain.Error{Code: "VERSION_CONFLICT", Message: "Review the current repository version"}
		}
	}
	if err := gitengine.ValidateRepository(state.Repository); err != nil {
		return state, invalid(err.Error())
	}
	if declared, ok := c.config.Repositories[state.Repository.Name]; ok {
		credential := ""
		if state.Repository.Remote.CredentialRef != nil {
			credential = string(*state.Repository.Remote.CredentialRef)
		}
		declaredCredential := ""
		if declared.CredentialRef != nil {
			declaredCredential = *declared.CredentialRef
		}
		if declared.RemoteURL != state.Repository.Remote.URL || declared.UpstreamBranch != state.Repository.Upstream.Branch || credential != declaredCredential {
			return state, &domain.Error{Code: "CONFIGURATION_CONFLICT", Message: "Repository settings are owned by runtime configuration"}
		}
	}
	return state, nil
}

func (c *ClusterNode) repositoryDefinitions(journal engine.LocalStore) []engine.TaskDefinition {
	prepare := func(ctx context.Context, e engine.Execution) error { return c.prepareRepository(ctx, journal, e) }
	definitions := []engine.TaskDefinition{}
	for _, kind := range []string{"repository.import", "repository.fetch", "repository.remote.set"} {
		// Acceptance and local Git work share repository exclusion.
		definitions = append(definitions, engine.TaskDefinition{Kind: kind, Phases: []engine.Phase{
			{Name: "fetch", Interruptible: true, Resources: []engine.Resource{{Key: "repositories", Capacity: 1}}, Run: prepare, Reconcile: prepare},
			{Name: "accept", Interruptible: false, Resources: []engine.Resource{{Key: "repositories", Capacity: 1}}, Run: c.acceptRepository, Reconcile: c.acceptRepository},
		}})
	}
	return definitions
}

func (c *ClusterNode) prepareRepository(ctx context.Context, journal engine.LocalStore, e engine.Execution) error {
	record, err := journal.Receipt(ctx, e.Task().CommandID)
	if err != nil {
		return err
	}
	_, value, err := Catalog().Decode(record.Envelope.Command)
	if err != nil {
		return err
	}
	c.repositoryMu.Lock()
	defer c.repositoryMu.Unlock()
	c.mu.Lock()
	state, err := c.repositoryInput(ctx, record.Envelope.Command, value)
	c.mu.Unlock()
	if err == nil {
		if _, done := state.Applied[record.Envelope.Command.ID]; !done && e.Task().Kind != "repository.remote.set" {
			fmt.Fprintln(e.Log(), "Fetching repository", state.Repository.ID, "branch", state.Repository.Upstream.Branch)
			var commit domain.Commit
			commit, err = c.git.Fetch(ctx, state.Repository)
			if err == nil {
				now := time.Now().UTC()
				state.Repository.Upstream.Commit = &commit
				state.Repository.Upstream.FetchedAt = &now
			}
		}
	}
	// Fetch affects only rebuildable local objects. A failed fetch is definitive,
	// never permission to accept an old cached head. Do not leak transport errors.
	if err != nil {
		var detail *domain.Error
		if !errors.As(err, &detail) {
			err = &domain.Error{Code: "SOURCE_UNAVAILABLE", Message: "Remote fetch failed; no upstream revision was accepted"}
		}
		return observeFailure(context.WithoutCancel(ctx), e, "fetch", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return e.Observed(ctx, domain.Checkpoint{Phase: "fetch", Evidence: data})
}

func (c *ClusterNode) acceptRepository(ctx context.Context, e engine.Execution) error {
	var proposed domain.RepositoryState
	for _, cp := range e.Task().Checkpoints {
		if cp.Phase == "fetch" {
			if err := json.Unmarshal(cp.Evidence, &proposed); err != nil {
				return err
			}
		}
	}
	if proposed.Repository.ID == "" {
		return invalid("Missing pinned repository checkpoint")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return err
	}
	current, revision, err := c.repositoryLocked(ctx, proposed.Repository.ID)
	var detail *domain.Error
	missing := errors.As(err, &detail) && detail.Code == "NOT_FOUND"
	if err != nil && !missing {
		return err
	}
	command := e.Task().CommandID
	result, done := current.Applied[command]
	if !done {
		if (missing && proposed.Repository.Version != 0) || (!missing && current.Repository.Version != proposed.Repository.Version) {
			failure := &domain.Error{Code: "VERSION_CONFLICT", Message: "Repository changed during fetch; accepted refs were not overwritten"}
			return observeFailure(ctx, e, "accept", failure)
		}
		proposed.Repository.Version++
		if e.Task().Kind == "repository.remote.set" {
			// A remote/branch change invalidates freshness, but preserves pinned old
			// source and refs until a subsequent successful explicit fetch.
			proposed.Repository.Upstream.FetchedAt = nil
			proposed.Repository.Upstream.Commit = nil
		} else {
			ref := domain.Reference{ID: sourceID("ref", string(proposed.Repository.ID), "upstream"), RepositoryID: proposed.Repository.ID, Kind: "upstream", Name: proposed.Repository.Upstream.Branch, Head: *proposed.Repository.Upstream.Commit, Publication: domain.RefPublication{State: "upstream"}}
			found := false
			for i := range proposed.Refs {
				if proposed.Refs[i].ID == ref.ID {
					proposed.Refs[i] = ref
					found = true
				}
			}
			if !found {
				proposed.Refs = append(proposed.Refs, ref)
			}
		}
		result = domain.RepositoryResult{RepositoryID: proposed.Repository.ID, Version: proposed.Repository.Version, Commit: proposed.Repository.Upstream.Commit}
		if proposed.Applied == nil {
			proposed.Applied = map[domain.ID]domain.RepositoryResult{}
		}
		proposed.Applied[command] = result
		if _, err = writeRecord(ctx, c.transport.Store, recordKey("repository", proposed.Repository.ID), revision, proposed); err != nil {
			return err
		}
	}
	return completePhase(ctx, e, "accept", result)
}
