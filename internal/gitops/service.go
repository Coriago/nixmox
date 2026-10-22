// Package gitops owns accepted repository revisions and incremental source artifacts.
package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/state"
)

type Service struct {
	State state.Store
	Git   *gitstore.Git
}

func missing(err error) bool { var e *domain.Error; return errors.As(err, &e) && e.Code == "NOT_FOUND" }
func (s *Service) Repository(ctx context.Context, id string) (domain.Repository, error) {
	var r domain.Repository
	_, err := s.State.Get(ctx, "repository."+id, &r)
	return r, err
}
func (s *Service) Repositories(ctx context.Context, limit, cursor string) (domain.Page[domain.Repository], error) {
	entries, err := s.State.List(ctx, "repository.")
	if err != nil {
		return domain.Page[domain.Repository]{}, err
	}
	rows := []domain.Repository{}
	for _, e := range entries {
		var r domain.Repository
		if domain.StrictJSON(e.Data, &r) != nil {
			return domain.Page[domain.Repository]{}, domain.Err("UNAVAILABLE", "Repository catalog is invalid.", 503)
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return domain.Paginate(rows, "repositories", limit, cursor, func(r domain.Repository) string { return r.ID })
}
func (s *Service) Artifact(ctx context.Context, id string) (domain.Artifact, error) {
	var a domain.Artifact
	_, err := s.State.Get(ctx, "artifact."+id, &a)
	return a, err
}
func (s *Service) Source(ctx context.Context, id, ref, commit string) (domain.Repository, string, string, error) {
	r, err := s.Repository(ctx, id)
	if err != nil {
		return r, "", "", err
	}
	if ref != "" && commit != "" {
		return r, "", "", domain.Err("INVALID_SOURCE", "Choose a ref or a commit, not both.", 422)
	}
	if commit != "" {
		if !gitstore.CommitPattern.MatchString(commit) {
			return r, "", "", domain.Err("INVALID_SOURCE", "Invalid commit.", 422)
		}
		return r, "", commit, nil
	}
	if ref == "" {
		ref = gitstore.StableID("ref_", id+"/upstream")
	}
	for _, v := range r.Refs {
		if v.ID == ref {
			return r, ref, v.Head, nil
		}
	}
	return r, "", "", domain.Err("NOT_FOUND", "Repository ref not found.", 404)
}

// Materialize never substitutes a newer upstream commit for a pinned base.
func (s *Service) Materialize(ctx context.Context, r domain.Repository, commit string) error {
	if s.Git.HasCommit(ctx, r.ID, commit) {
		return nil
	}
	for _, id := range r.Artifacts {
		a, err := s.Artifact(ctx, id)
		if err != nil {
			return err
		}
		if a.Commit != commit {
			continue
		}
		if a.Storage.State != "stored" {
			return domain.Err("SOURCE_UNAVAILABLE", "Required source artifact has been deleted.", 503)
		}
		if err = s.Git.FetchCommit(ctx, r.ID, r.Remote.URL, a.RemoteBaseCommit); err != nil {
			return err
		}
		// Every generated increment contains the complete local delta against its
		// remote prerequisite, so it has no hidden dependency on a peer checkout.
		f, err := os.CreateTemp(s.Git.Root, "materialize-*.bundle")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		err = s.State.ReadArtifact(ctx, a.Bundle.ObjectKey, a.Bundle.SHA256, a.Bundle.Bytes, f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return s.Git.Import(ctx, r.ID, name, commit)
	}
	return s.Git.FetchCommit(ctx, r.ID, r.Remote.URL, commit)
}
func (s *Service) Diff(ctx context.Context, id, from, to string) (any, error) {
	r, err := s.Repository(ctx, id)
	if err != nil {
		return nil, err
	}
	// Read queries deliberately do not fetch or mutate a materialization.
	diff, err := s.Git.Diff(ctx, r.ID, from, to)
	return struct {
		From string `json:"from"`
		To   string `json:"to"`
		Diff string `json:"diff"`
	}{from, to, diff}, err
}

type Change struct {
	RepositoryID      string
	RepositoryVersion int64
	BaseCommit        string
	RefID             string
	CommandID         string
	TaskID            string
	ValidationID      string
	Configuration     domain.Configuration
	Files             map[string][]byte
	CreatedAt         time.Time
}

func (s *Service) Accept(ctx context.Context, c Change) (domain.SourceAcceptance, error) {
	var r domain.Repository
	rev, err := s.State.Get(ctx, "repository."+c.RepositoryID, &r)
	if err != nil {
		return domain.SourceAcceptance{}, err
	}
	if prior, ok := r.AcceptedCommands[c.CommandID]; ok {
		return prior, nil
	}
	if r.Version != c.RepositoryVersion {
		return domain.SourceAcceptance{}, domain.Err("VERSION_CONFLICT", "Repository changed after draft review.", 409)
	}
	if r.Upstream.Commit == nil {
		return domain.SourceAcceptance{}, domain.Err("SOURCE_UNAVAILABLE", "Repository has no pinned remote base.", 503)
	}
	if err = s.Materialize(ctx, r, c.BaseCommit); err != nil {
		return domain.SourceAcceptance{}, err
	}
	base := c.BaseCommit
	// An upstream fetch may have advanced since the draft base was created. Use
	// the original remote prerequisite when the draft extends a local increment.
	for _, id := range r.Artifacts {
		a, e := s.Artifact(ctx, id)
		if e != nil {
			return domain.SourceAcceptance{}, e
		}
		if a.Commit == c.BaseCommit {
			base = a.RemoteBaseCommit
			break
		}
	}
	if c.RefID == "" || c.BaseCommit != base {
		if !s.Git.HasCommit(ctx, r.ID, base) {
			if err = s.Git.FetchCommit(ctx, r.ID, r.Remote.URL, base); err != nil {
				return domain.SourceAcceptance{}, err
			}
		}
	}
	inc, err := s.Git.Commit(ctx, r.ID, c.BaseCommit, base, c.CommandID, c.Files, c.CreatedAt)
	if err != nil {
		return domain.SourceAcceptance{}, err
	}
	artID := gitstore.StableID("art_", r.ID+"/"+c.CommandID)
	a := domain.Artifact{SchemaVersion: 1, ID: artID, Version: 1, RepositoryID: r.ID, CommandID: c.CommandID, Commit: inc.Commit, RemoteBaseCommit: base, Prerequisites: inc.Prerequisites, TaskIDs: []string{c.TaskID}}
	a.Bundle.ObjectKey = r.ID + "/" + artID + "/" + inc.Digest
	a.Bundle.SHA256 = inc.Digest
	a.Bundle.Bytes = inc.Bytes
	a.Storage.State = "stored"
	a.Retention.State = "unpublished"
	a.Retention.Pins = []string{}
	if c.ValidationID != "" {
		a.ValidationID = &c.ValidationID
	}
	f, err := os.Open(inc.Path)
	if err != nil {
		return domain.SourceAcceptance{}, err
	}
	err = s.State.PutArtifact(ctx, a.Bundle.ObjectKey, f, inc.Bytes, inc.Digest)
	f.Close()
	if err != nil {
		return domain.SourceAcceptance{}, err
	}
	var old domain.Artifact
	_, err = s.State.Get(ctx, "artifact."+artID, &old)
	if missing(err) {
		_, err = s.State.CAS(ctx, "artifact."+artID, a, 0)
	} else if err == nil && (old.Commit != a.Commit || old.Bundle.SHA256 != a.Bundle.SHA256) {
		err = domain.Err("ARTIFACT_CONFLICT", "Command artifact has different source bytes.", 409)
	}
	if err != nil {
		return domain.SourceAcceptance{}, err
	}
	refID := c.RefID
	index := -1
	for i, v := range r.Refs {
		if v.ID == refID && v.Kind == "change" {
			index = i
		}
	}
	if index < 0 {
		refID = gitstore.StableID("ref_", r.ID+"/"+c.CommandID)
		upstream := gitstore.StableID("ref_", r.ID+"/upstream")
		r.Refs = append(r.Refs, domain.SourceRef{ID: refID, RepositoryID: r.ID, Kind: "change", Name: "change/" + c.Configuration.Name, UpstreamRefID: &upstream, Publication: domain.SourcePublication{State: "local"}})
		index = len(r.Refs) - 1
	}
	r.Refs[index].Head = inc.Commit
	r.Refs[index].Ahead, r.Refs[index].Behind, err = s.Git.Counts(ctx, r.ID, *r.Upstream.Commit, inc.Commit)
	if err != nil {
		return domain.SourceAcceptance{}, err
	}
	cfg := c.Configuration
	cfg.RefID = refID
	cfg.InspectedCommit = inc.Commit
	found := false
	for i, v := range r.Configurations {
		if v.ID == cfg.ID {
			cfg.Version = v.Version + 1
			r.Configurations[i] = cfg
			found = true
			break
		}
		if v.Name == cfg.Name {
			return domain.SourceAcceptance{}, domain.Err("NAME_CONFLICT", "Configuration name is already registered.", 409)
		}
	}
	if !found {
		cfg.Version = 1
		r.Configurations = append(r.Configurations, cfg)
	}
	accepted := domain.SourceAcceptance{Commit: inc.Commit, RefID: refID, ArtifactID: artID, ConfigurationID: cfg.ID}
	if r.AcceptedCommands == nil {
		r.AcceptedCommands = map[string]domain.SourceAcceptance{}
	}
	r.AcceptedCommands[c.CommandID] = accepted
	r.Artifacts = append(r.Artifacts, artID)
	r.Version++
	_, err = s.State.CAS(ctx, "repository."+r.ID, r, rev)
	return accepted, err
}

func encoded(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
