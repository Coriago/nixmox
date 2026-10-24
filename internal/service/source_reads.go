package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"nixmox/internal/domain"
	gitengine "nixmox/internal/engine/git"
)

func (c *ClusterNode) repositories(ctx context.Context) ([]domain.RepositoryState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport.Store == nil {
		return nil, domain.Unavailable("repository metadata")
	}
	items, err := listRecords[domain.RepositoryState](ctx, c.transport.Store.KV, "repository.*")
	if err != nil {
		return nil, err
	}
	slices.SortFunc(items, func(a, b domain.RepositoryState) int {
		return cmp.Or(strings.Compare(a.Repository.Name, b.Repository.Name), cmp.Compare(a.Repository.ID, b.Repository.ID))
	})
	return items, nil
}

func configurationSource(state domain.RepositoryState, refID domain.ID, commit domain.Commit) (domain.Source, error) {
	if commit != "" && !gitengine.ValidCommit(commit) {
		return domain.Source{}, invalid("Commit must be an exact supported Git object ID")
	}
	if refID != "" && commit != "" {
		return domain.Source{}, invalid("ref_id and commit are mutually exclusive")
	}
	if commit == "" {
		if refID == "" {
			if state.Repository.Upstream.Commit == nil {
				return domain.Source{}, &domain.Error{Code: "SOURCE_UNAVAILABLE", Message: "Fetch this repository before selecting its upstream catalog"}
			}
			refID = sourceID("ref", string(state.Repository.ID), "upstream")
		}
		for _, ref := range state.Refs {
			if ref.ID == refID {
				commit = ref.Head
			}
		}
		if commit == "" {
			return domain.Source{}, notFound()
		}
	}
	source := domain.Source{RepositoryID: state.Repository.ID, Commit: commit}
	if refID != "" {
		source.RefID = &refID
	}
	return source, nil
}

// withSource owns both Git exclusion and the temporary checkout lifetime.
func (c *ClusterNode) withSource(ctx context.Context, source domain.Source, inspect func(string) error) error {
	c.repositoryMu.Lock()
	defer c.repositoryMu.Unlock()
	tree, err := c.git.Materialize(ctx, source)
	if err != nil {
		return err
	}
	defer tree.Close()
	return inspect(tree.Path())
}

func (c *ClusterNode) configurations(ctx context.Context, state domain.RepositoryState, refID domain.ID, commit domain.Commit) ([]domain.Configuration, error) {
	source, err := configurationSource(state, refID, commit)
	if err != nil {
		return nil, err
	}
	items := []domain.Configuration{}
	err = c.withSource(ctx, source, func(path string) error {
		descriptors, err := c.nix.Configurations(ctx, source, path)
		if err != nil {
			return &domain.Error{Code: "CATALOG_UNAVAILABLE", Message: "Pinned configuration catalog could not be inspected; no revision was substituted"}
		}
		for _, d := range descriptors {
			var ref domain.ID
			if source.RefID != nil {
				ref = *source.RefID
			}
			items = append(items, domain.Configuration{Record: domain.Record{SchemaVersion: domain.SchemaVersion, ID: sourceID("cfg", string(state.Repository.ID), d.Name), Version: state.Repository.Version}, RepositoryID: state.Repository.ID, Name: d.Name, FlakeDirectory: d.FlakeDirectory, FlakeAttribute: d.FlakeAttribute, RefID: ref, Readiness: "unvalidated", InspectedCommit: source.Commit})
		}
		return nil
	})
	if !c.leaf() {
		err = c.onSourceHolder(ctx, state.Repository, err, func(holder domain.ID) error {
			return c.callResult(ctx, holder, "source.configurations", sourceCatalogRequest{State: state, RefID: refID, Commit: commit}, false, &items)
		})
	}
	return items, err
}

// Only missing local source permits holder fallback; evaluation failures and
// revision conflicts must never select another source or hide their original error.
func (c *ClusterNode) onSourceHolder(ctx context.Context, repository domain.Repository, localErr error, inspect func(domain.ID) error) error {
	var detail *domain.Error
	if !errors.As(localErr, &detail) || detail.Code != "SOURCE_UNAVAILABLE" || repository.Remote.CredentialRef == nil {
		return localErr
	}
	c.mu.Lock()
	record, _, err := c.credentialLocked(ctx, *repository.Remote.CredentialRef)
	var holder domain.ID
	if err == nil {
		holder, err = c.credentialHolderLocked(record)
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if holder == c.keys.Identity.NodeID {
		return localErr
	}
	return inspect(holder)
}

type sourceCatalogRequest struct {
	State  domain.RepositoryState `json:"state"`
	RefID  domain.ID              `json:"ref_id"`
	Commit domain.Commit          `json:"commit"`
}

func (c *ClusterNode) readSources(ctx context.Context, request ReadRequest) (json.RawMessage, error) {
	// Bound evaluation and watch initialization independently of a disconnected UI.
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if request.Collection == "repositories" && request.ID != nil {
		state, err := c.repository(ctx, *request.ID)
		if err != nil {
			return nil, err
		}
		if request.Subresource == "" {
			return json.Marshal(state.Repository)
		}
		if request.Subresource != "refs" {
			return nil, domain.Unavailable(request.Subresource)
		}
		slices.SortFunc(state.Refs, func(a, b domain.Reference) int {
			return cmp.Or(strings.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
		})
		page, err := pageItems(state.Refs, request.Page, "repositories/{id}/refs", func(r domain.Reference) []string { return []string{string(state.Repository.ID), r.Name, string(r.ID)} })
		return marshalResult(page, err)
	}
	states, err := c.repositories(ctx)
	if err != nil {
		return nil, err
	}
	if request.Collection == "repositories" {
		items := make([]domain.Repository, 0, len(states))
		for _, state := range states {
			items = append(items, state.Repository)
		}
		page, err := pageItems(items, request.Page, "repositories", func(r domain.Repository) []string { return []string{r.Name, string(r.ID)} })
		return marshalResult(page, err)
	}
	filters := request.Page.Filters
	repository, ref, commit := domain.ID(filters["repository_id"]), domain.ID(filters["ref_id"]), domain.Commit(filters["commit"])
	if (ref != "" || commit != "") && repository == "" {
		return nil, invalid("Revision selection requires repository_id")
	}
	if ref != "" && commit != "" {
		return nil, invalid("ref_id and commit are mutually exclusive")
	}
	items := []domain.Configuration{}
	revisions := []string{}
	found := repository == ""
	var inspectionError error
	for _, state := range states {
		if repository != "" && repository != state.Repository.ID {
			continue
		}
		found = true
		configs, err := c.configurations(ctx, state, ref, commit)
		if err != nil {
			if request.ID == nil {
				return nil, err
			}
			inspectionError = err
			continue
		}
		if request.ID != nil {
			for _, item := range configs {
				if item.ID == *request.ID {
					return json.Marshal(item)
				}
			}
		}
		items = append(items, configs...)
		// Bind every page to the complete inspected catalog, not only its last row.
		revisions = append(revisions, fmt.Sprintf("%s:%d", state.Repository.ID, state.Repository.Version))
	}
	if !found {
		return nil, notFound()
	}
	slices.SortFunc(items, func(a, b domain.Configuration) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	if request.ID != nil {
		if inspectionError != nil {
			return nil, inspectionError
		}
		return nil, notFound()
	}
	// Include stable revision identities in the cursor position. A changed catalog
	// expires the cursor rather than mixing commits across pages.
	revision := strings.Join(revisions, ";")
	page, err := pageItems(items, request.Page, "configurations", func(v domain.Configuration) []string {
		return []string{revision, v.Name, string(v.ID), string(v.InspectedCommit)}
	})
	return marshalResult(page, err)
}
