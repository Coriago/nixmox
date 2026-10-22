package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"
	"unicode/utf8"

	"nixmox/internal/domain"
	"nixmox/internal/gitops"
	"nixmox/internal/gitstore"
	"nixmox/internal/render"
	"nixmox/internal/service"
)

type payload struct {
	RepositoryID string         `json:"repository_id,omitempty"`
	TemplateID   string         `json:"template_id,omitempty"`
	RefID        string         `json:"ref_id,omitempty"`
	BaseCommit   string         `json:"base_commit,omitempty"`
	DraftID      string         `json:"draft_id,omitempty"`
	Values       map[string]any `json:"values,omitempty"`
	File         *struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"file,omitempty"`
}
type resolution struct {
	RepositoryID      string    `json:"repository_id"`
	RepositoryVersion int64     `json:"repository_version"`
	BaseCommit        string    `json:"base_commit"`
	RefID             string    `json:"ref_id"`
	TemplateID        string    `json:"template_id"`
	DraftVersion      int64     `json:"draft_version"`
	At                time.Time `json:"at"`
}

func (s *Service) Operations() []service.Operation {
	ops := []service.Operation{}
	for _, kind := range []string{"configuration.generate", "configuration.draft.edit", "configuration.validate", "configuration.commit"} {
		op := service.Operation{Kind: kind, Scope: "cluster", Timeout: 3 * time.Minute, SafeRetry: true}
		op.Validate = func(c domain.Command) error {
			var p payload
			if domain.StrictJSON(c.Payload, &p) != nil {
				return domain.Err("INVALID_PAYLOAD", "Invalid configuration fields.", 422)
			}
			if c.Type == "configuration.generate" {
				if !domain.SafeID.MatchString(p.RepositoryID) || p.DraftID != "" || p.File != nil || len(c.ExpectedVersions) != 0 || p.Values == nil || (p.RefID != "" && p.BaseCommit != "") {
					return domain.Err("INVALID_PAYLOAD", "Generation requires repository_id and typed values.", 422)
				}
			} else {
				if !domain.SafeID.MatchString(p.DraftID) || p.RepositoryID != "" || p.TemplateID != "" || p.RefID != "" || p.BaseCommit != "" {
					return domain.Err("INVALID_PAYLOAD", "A draft_id is required.", 422)
				}
				switch c.Type {
				case "configuration.draft.edit":
					if (p.Values == nil) == (p.File == nil) || len(c.ExpectedVersions) != 1 || c.ExpectedVersions["draft:"+p.DraftID] < 1 {
						return domain.Err("INVALID_PAYLOAD", "Edit requires one values/file patch and the reviewed draft version.", 422)
					}
					if p.File != nil && (!gitstore.SafePath(p.File.Path) || !utf8.ValidString(p.File.Content)) {
						return domain.Err("INVALID_PATH", "Invalid draft text file.", 422)
					}
				case "configuration.validate":
					if p.Values != nil || p.File != nil || len(c.ExpectedVersions) != 0 {
						return domain.Err("INVALID_PAYLOAD", "Validation accepts only draft_id.", 422)
					}
				case "configuration.commit":
					if p.Values != nil || p.File != nil || len(c.ExpectedVersions) != 2 || c.ExpectedVersions["draft:"+p.DraftID] < 1 {
						return domain.Err("VERSION_REQUIRED", "Commit requires reviewed draft and repository versions.", 422)
					}
				}
			}
			return nil
		}
		op.Resolve = s.resolve
		op.Exclusions = func(c domain.Command, b json.RawMessage) ([]string, error) {
			var p payload
			_ = json.Unmarshal(c.Payload, &p)
			id := p.DraftID
			if id == "" {
				id = draftID(c.ID)
			}
			return []string{"draft:" + id}, nil
		}
		op.Run = s.run
		op.Reconcile = s.run
		ops = append(ops, op)
	}
	return ops
}
func (s *Service) resolve(ctx context.Context, c domain.Command) (json.RawMessage, error) {
	var p payload
	_ = json.Unmarshal(c.Payload, &p)
	identity, err := s.Local.Identity()
	if err != nil {
		return nil, err
	}
	if c.TargetID != identity.NodeID {
		return nil, domain.Err("OWNER_REQUIRED", "Use the source/draft owner console for this command.", 409)
	}
	v := resolution{At: time.Now().UTC()}
	if c.Type == "configuration.generate" {
		r, ref, commit, err := s.Sources.Source(ctx, p.RepositoryID, p.RefID, p.BaseCommit)
		if err != nil {
			return nil, err
		}
		t, bodies, err := s.selected(ctx, r.ID, commit, p.TemplateID)
		if err != nil {
			return nil, err
		}
		if _, err = render.Render(t, p.Values, bodies); err != nil {
			return nil, err
		}
		v.RepositoryID = r.ID
		v.RepositoryVersion = r.Version
		v.BaseCommit = commit
		v.RefID = ref
		v.TemplateID = t.ID
	} else {
		s.mu.Lock()
		d, err := s.load(p.DraftID)
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if d.Commit != nil {
			return nil, domain.Err("INVALID_STATE", "Committed drafts are immutable; create a new draft.", 409)
		}
		if c.Type != "configuration.validate" && c.ExpectedVersions["draft:"+d.ID] != d.Version {
			return nil, domain.Err("VERSION_CONFLICT", "Draft changed after review.", 409)
		}
		if c.Type == "configuration.commit" && c.ExpectedVersions["repository:"+d.RepositoryID] != d.RepositoryVersion {
			return nil, domain.Err("VERSION_CONFLICT", "Commit must use the draft's reviewed repository version.", 409)
		}
		v.DraftVersion = d.Version
		v.RepositoryID = d.RepositoryID
		v.RepositoryVersion = d.RepositoryVersion
		v.BaseCommit = d.BaseCommit
		v.RefID = d.RefID
		v.TemplateID = d.TemplateID
	}
	b, _ := json.Marshal(v)
	return b, nil
}
func (s *Service) run(ctx context.Context, e *service.Execution) (any, error) {
	var p payload
	var v resolution
	if domain.StrictJSON(e.Command.Payload, &p) != nil || domain.StrictJSON(e.Resolved, &v) != nil {
		return nil, domain.Err("INVALID_STATE", "Configuration resolution is invalid.", 409)
	}
	id := p.DraftID
	if id == "" {
		id = draftID(e.Command.ID)
	}
	s.mu.Lock()
	d, err := s.load(id)
	s.mu.Unlock()
	if err == nil {
		if _, ok := d.AcceptedCommands[e.Command.ID]; ok {
			return d.result(), nil
		}
	}
	if e.Command.Type == "configuration.generate" {
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != "DRAFT_UNAVAILABLE" || de.Status != 404 {
			return nil, domain.Err("DRAFT_CONFLICT", "Draft identity already exists or is unavailable.", 409)
		}
		identity, err := s.Local.Identity()
		if err != nil {
			return nil, err
		}
		if identity.ClusterID == nil {
			return nil, domain.Err("QUORUM_UNAVAILABLE", "Cluster identity unavailable.", 503)
		}
		d = Draft{SchemaVersion: 1, ID: id, Version: 1, OwnerNodeID: identity.NodeID, ClusterID: *identity.ClusterID, MembershipEpoch: identity.MembershipEpoch, RepositoryID: v.RepositoryID, RepositoryVersion: v.RepositoryVersion, BaseCommit: v.BaseCommit, RefID: v.RefID, TemplateID: v.TemplateID, CreatedAt: v.At, Baseline: map[string]string{}, ManuallyEdited: map[string]bool{}, Regenerated: map[string]string{}, AcceptedCommands: map[string]int64{}}
		generated, t, err := s.render(ctx, d, p.Values)
		if err != nil {
			return nil, err
		}
		d.Values = generated.Values
		d.Configuration = generated.Configuration
		d.Files = generated.Files
		d.PreparationTemplates = t.PreparationTemplates
		for _, f := range d.Files {
			before, readErr := s.Sources.Git.ReadFile(ctx, d.RepositoryID, d.BaseCommit, f.Path)
			if readErr == nil && string(before) != f.Content {
				return nil, domain.Err("FILE_CONFLICT", "Generation would overwrite an existing file: "+f.Path, 409)
			}
			if readErr != nil {
				var de *domain.Error
				if !errors.As(readErr, &de) || de.Code != "NOT_FOUND" {
					return nil, readErr
				}
			}
			d.Baseline[f.Path] = f.Content
		}
		d.Validation = Validation{State: "unvalidated", Missing: []string{}, Errors: []string{}}
	} else {
		if err != nil {
			return nil, err
		}
		if d.Version != v.DraftVersion {
			return nil, domain.Err("VERSION_CONFLICT", "Draft changed after command admission.", 409)
		}
		switch e.Command.Type {
		case "configuration.draft.edit":
			if p.File != nil {
				found := false
				for i, f := range d.Files {
					if f.Path == p.File.Path {
						d.Files[i].Content = p.File.Content
						d.ManuallyEdited[f.Path] = true
						delete(d.Regenerated, f.Path)
						found = true
						break
					}
				}
				if !found {
					return nil, domain.Err("INVALID_PATH", "Only existing generated text outputs can be edited.", 422)
				}
			} else {
				values := map[string]any{}
				for k, v := range d.Values {
					values[k] = v
				}
				for k, v := range p.Values {
					values[k] = v
				}
				generated, _, err := s.render(ctx, d, values)
				if err != nil {
					return nil, err
				}
				next := map[string]render.File{}
				for _, f := range generated.Files {
					if _, known := d.Baseline[f.Path]; !known {
						before, readErr := s.Sources.Git.ReadFile(ctx, d.RepositoryID, d.BaseCommit, f.Path)
						if readErr == nil && string(before) != f.Content {
							return nil, domain.Err("FILE_CONFLICT", "Form change would overwrite an existing file: "+f.Path, 409)
						}
						if readErr != nil {
							var failure *domain.Error
							if !errors.As(readErr, &failure) || failure.Code != "NOT_FOUND" {
								return nil, readErr
							}
						}
					}
					next[f.Path] = f
				}
				for _, old := range d.Files {
					if d.ManuallyEdited[old.Path] {
						if fresh, ok := next[old.Path]; !ok || fresh.Content != d.Baseline[old.Path] {
							d.Regenerated[old.Path] = fresh.Content
						}
						next[old.Path] = old
					}
				}
				d.Files = []render.File{}
				for _, f := range next {
					d.Files = append(d.Files, f)
				}
				sortFiles(d.Files)
				for _, f := range generated.Files {
					d.Baseline[f.Path] = f.Content
				}
				d.Values = generated.Values
				d.Configuration = generated.Configuration
			}
			d.Version++
			d.Validation = Validation{State: "unvalidated", Missing: []string{}, Errors: []string{}}
		case "configuration.validate":
			d.Validation, err = s.validate(ctx, d)
			if err != nil {
				return nil, err
			}
		case "configuration.commit":
			if len(d.Regenerated) != 0 {
				return nil, domain.Err("MERGE_REVIEW_REQUIRED", "Resolve regenerated text for manually edited files before committing.", 409)
			}
			validation, err := s.validate(ctx, d)
			if err != nil {
				return nil, err
			}
			d.Validation = validation
			if validation.State == "invalid" {
				return nil, domain.Err("VALIDATION_FAILED", "Draft has syntax or export errors; validate and review its files.", 422)
			}
			files := map[string][]byte{}
			for _, f := range d.Files {
				files[f.Path] = []byte(f.Content)
			}
			cfg := domain.Configuration{SchemaVersion: 1, ID: gitstore.StableID("cfg_", d.RepositoryID+"/"+d.Configuration.Name), RepositoryID: d.RepositoryID, Name: d.Configuration.Name, FlakeDirectory: d.Configuration.FlakeDirectory, FlakeAttribute: d.Configuration.FlakeAttribute, Readiness: validation.State, PreparationTemplates: d.PreparationTemplates}
			if validation.State == "incomplete" {
				cfg.Readiness = "needs_preparation"
			}
			accepted, err := s.Sources.Accept(ctx, gitops.Change{RepositoryID: d.RepositoryID, RepositoryVersion: d.RepositoryVersion, BaseCommit: d.BaseCommit, RefID: d.RefID, CommandID: e.Command.ID, TaskID: e.TaskID, ValidationID: validation.ID, Configuration: cfg, Files: files, CreatedAt: v.At})
			if err != nil {
				return nil, err
			}
			d.Commit = &accepted
		}
	}
	d.AcceptedCommands[e.Command.ID] = d.Version
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Command.Type != "configuration.generate" {
		current, err := s.load(d.ID)
		if err != nil {
			return nil, err
		}
		if current.Version != v.DraftVersion {
			return nil, domain.Err("VERSION_CONFLICT", "Draft changed during execution.", 409)
		}
	} else {
		if _, err := os.Stat(s.Local.Root + "/drafts/" + d.ID + ".json"); err == nil {
			return nil, domain.Err("DRAFT_CONFLICT", "Draft was concurrently created.", 409)
		}
	}
	d.ExpectedVersions = map[string]int64{"draft:" + d.ID: d.Version, "repository:" + d.RepositoryID: d.RepositoryVersion}
	if err = s.save(d); err != nil {
		return nil, err
	}
	return d.result(), nil
}
func (d Draft) result() any {
	return struct {
		DraftID          string                   `json:"draft_id"`
		Version          int64                    `json:"version"`
		OwnerNodeID      string                   `json:"owner_node_id"`
		Configuration    render.Configuration     `json:"configuration"`
		Validation       Validation               `json:"validation"`
		ExpectedVersions map[string]int64         `json:"expected_versions"`
		Commit           *domain.SourceAcceptance `json:"commit"`
	}{d.ID, d.Version, d.OwnerNodeID, d.Configuration, d.Validation, d.ExpectedVersions, d.Commit}
}
