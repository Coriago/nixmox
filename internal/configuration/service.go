// Package configuration owns pinned templates, private drafts and source validation.
package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/gitops"
	"nixmox/internal/gitstore"
	"nixmox/internal/render"
	"nixmox/internal/storage"
)

type Service struct {
	Sources *gitops.Service
	Local   *storage.Store
	mu      sync.Mutex
}
type Validation struct {
	ID           string   `json:"id"`
	State        string   `json:"state"`
	DraftVersion int64    `json:"draft_version"`
	Missing      []string `json:"missing"`
	Errors       []string `json:"errors"`
}
type Draft struct {
	SchemaVersion        int                      `json:"schema_version"`
	ID                   string                   `json:"id"`
	Version              int64                    `json:"version"`
	OwnerNodeID          string                   `json:"owner_node_id"`
	ClusterID            string                   `json:"cluster_id"`
	MembershipEpoch      int                      `json:"membership_epoch"`
	RepositoryID         string                   `json:"repository_id"`
	RepositoryVersion    int64                    `json:"repository_version"`
	BaseCommit           string                   `json:"base_commit"`
	RefID                string                   `json:"ref_id"`
	TemplateID           string                   `json:"template_id"`
	Values               map[string]any           `json:"values"`
	Configuration        render.Configuration     `json:"configuration"`
	Files                []render.File            `json:"files"`
	Baseline             map[string]string        `json:"baseline"`
	ManuallyEdited       map[string]bool          `json:"manually_edited"`
	Regenerated          map[string]string        `json:"regenerated"`
	PreparationTemplates map[string][]string      `json:"preparation_templates"`
	Validation           Validation               `json:"validation"`
	ExpectedVersions     map[string]int64         `json:"expected_versions"`
	AssignedHostID       *string                  `json:"assigned_host_id"`
	CreatedAt            time.Time                `json:"created_at"`
	AcceptedCommands     map[string]int64         `json:"accepted_commands"`
	Commit               *domain.SourceAcceptance `json:"commit"`
}

func New(sources *gitops.Service, local *storage.Store) (*Service, error) {
	if err := os.MkdirAll(local.Root+"/drafts", 0700); err != nil {
		return nil, err
	}
	return &Service{Sources: sources, Local: local}, nil
}
func (s *Service) load(id string) (Draft, error) {
	var d Draft
	if !domain.SafeID.MatchString(id) {
		return d, domain.Err("INVALID_ID", "Invalid draft ID.", 422)
	}
	err := s.Local.Read("drafts/"+id+".json", &d)
	if errors.Is(err, os.ErrNotExist) {
		return d, domain.Err("DRAFT_UNAVAILABLE", "Draft is unavailable on this node; use its owner console.", 404)
	}
	if err != nil {
		return d, domain.Err("DRAFT_UNAVAILABLE", "Private draft storage is unavailable.", 503)
	}
	identity, err := s.Local.Identity()
	if err != nil {
		return d, err
	}
	if identity.ClusterID == nil || *identity.ClusterID != d.ClusterID || identity.MembershipEpoch != d.MembershipEpoch {
		return d, domain.Err("MEMBERSHIP_CHANGED", "Draft belongs to another membership epoch.", 409)
	}
	d.ExpectedVersions = map[string]int64{"draft:" + d.ID: d.Version, "repository:" + d.RepositoryID: d.RepositoryVersion}
	return d, nil
}
func (s *Service) save(d Draft) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return domain.Err("STORAGE_LIMIT", "Private draft exceeds 4 MiB.", 507)
	}
	return s.Local.WriteBytes("drafts/"+d.ID+".json", b)
}
func (s *Service) Draft(ctx context.Context, id string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(id)
}
func (s *Service) DraftDiff(ctx context.Context, id string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load(id)
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, f := range d.Files {
		before, err := s.Sources.Git.ReadFile(ctx, d.RepositoryID, d.BaseCommit, f.Path)
		if err != nil {
			var e *domain.Error
			if !errors.As(err, &e) || e.Code != "NOT_FOUND" {
				return nil, err
			}
		}
		rows = append(rows, map[string]any{"path": f.Path, "before": string(before), "after": f.Content, "manually_edited": d.ManuallyEdited[f.Path]})
	}
	return map[string]any{"id": d.ID, "version": d.Version, "base_commit": d.BaseCommit, "files": rows, "expected_versions": d.ExpectedVersions}, nil
}

type TemplateSet struct {
	RepositoryID string            `json:"repository_id"`
	Commit       string            `json:"inspected_commit"`
	Items        []render.Template `json:"items"`
}
type TemplateBundle struct {
	Template  render.Template   `json:"template"`
	Templates map[string]string `json:"templates"`
	Commit    string            `json:"inspected_commit"`
}

func (s *Service) Template(ctx context.Context, id, templateID, commit string) (any, error) {
	_, _, pin, err := s.Sources.Source(ctx, id, "", commit)
	if err != nil {
		return nil, err
	}
	t, bodies, err := s.selected(ctx, id, pin, templateID)
	return TemplateBundle{t, bodies, pin}, err
}

func (s *Service) templates(ctx context.Context, id, commit string) ([]render.Template, map[string]string, error) {
	files, err := s.Sources.Git.Files(ctx, id, commit, ".nixmox/templates")
	if err != nil {
		return nil, nil, err
	}
	rows := []render.Template{}
	locations := map[string]string{}
	for _, name := range files {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := s.Sources.Git.ReadFile(ctx, id, commit, name)
		if err != nil {
			return nil, nil, err
		}
		var t render.Template
		if domain.StrictJSON(data, &t) != nil {
			return nil, nil, domain.Err("INVALID_TEMPLATE", "Invalid template definition: "+name, 422)
		}
		if err = t.Validate(); err != nil {
			return nil, nil, err
		}
		if _, ok := locations[t.ID]; ok {
			return nil, nil, domain.Err("INVALID_TEMPLATE", "Duplicate template ID.", 422)
		}
		rows = append(rows, t)
		locations[t.ID] = path.Dir(name)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, locations, nil
}
func (s *Service) Templates(ctx context.Context, id, ref, commit string) (any, error) {
	_, _, pin, err := s.Sources.Source(ctx, id, ref, commit)
	if err != nil {
		return nil, err
	}
	rows, _, err := s.templates(ctx, id, pin)
	return TemplateSet{id, pin, rows}, err
}
func (s *Service) selected(ctx context.Context, id, commit, templateID string) (render.Template, map[string]string, error) {
	rows, locations, err := s.templates(ctx, id, commit)
	if err != nil {
		return render.Template{}, nil, err
	}
	choices := []render.Template{}
	for _, t := range rows {
		if t.Kind == "configuration" && (templateID == "" || templateID == t.ID) {
			choices = append(choices, t)
		}
	}
	if len(choices) == 0 {
		return render.Template{}, nil, domain.Err("TEMPLATE_UNAVAILABLE", "No configuration template is available at this revision.", 422)
	}
	if len(choices) > 1 {
		ids := []string{}
		for _, t := range choices {
			ids = append(ids, t.ID)
		}
		return render.Template{}, nil, domain.Err("TEMPLATE_SELECTION_REQUIRED", "Select a configuration template: "+strings.Join(ids, ", "), 422)
	}
	t := choices[0]
	bodies := map[string]string{}
	for _, o := range t.Outputs {
		b, err := s.Sources.Git.ReadFile(ctx, id, commit, path.Join(locations[t.ID], o.Template))
		if err != nil {
			return t, nil, err
		}
		bodies[o.Template] = string(b)
	}
	return t, bodies, nil
}
func (s *Service) render(ctx context.Context, d Draft, input map[string]any) (render.Result, render.Template, error) {
	t, bodies, err := s.selected(ctx, d.RepositoryID, d.BaseCommit, d.TemplateID)
	if err != nil {
		return render.Result{}, t, err
	}
	rendered, err := render.Render(t, input, bodies)
	return rendered, t, err
}
func draftID(command string) string { return gitstore.StableID("draft_", command) }
