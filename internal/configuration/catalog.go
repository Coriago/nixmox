package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/process"
	"nixmox/internal/render"
)

type descriptor struct {
	render.Configuration
	PreparationTemplates map[string][]string `json:"preparation_templates,omitempty"`
}

func (s *Service) catalog(ctx context.Context, r domain.Repository, ref, commit string) ([]domain.Configuration, error) {
	rows := []domain.Configuration{}
	b, err := s.Sources.Git.ReadFile(ctx, r.ID, commit, ".nixmox/configurations.json")
	descriptors := []descriptor{}
	if err == nil {
		var catalog struct {
			SchemaVersion  int          `json:"schema_version"`
			Configurations []descriptor `json:"configurations"`
		}
		if domain.StrictJSON(b, &catalog) != nil || catalog.SchemaVersion != 1 || len(catalog.Configurations) > 1000 {
			return nil, domain.Err("INVALID_CATALOG", "Invalid configuration descriptors.", 422)
		}
		descriptors = catalog.Configurations
	} else {
		var e *domain.Error
		if !errors.As(err, &e) || e.Code != "NOT_FOUND" {
			return nil, err
		}
		dir, err := s.Sources.Git.Worktree(ctx, r.ID, commit, gitstore.StableID("catalog_", commit))
		if err != nil {
			return nil, err
		}
		result, err := process.Run(ctx, process.Options{Timeout: 30 * time.Second, MaxOutput: 256 << 10}, "nix", "eval", "--json", "--no-write-lock-file", "--no-update-lock-file", "--option", "allow-import-from-derivation", "false", "--option", "accept-flake-config", "false", "path:"+filepath.Clean(dir)+"#nixosConfigurations", "--apply", "builtins.attrNames")
		names := []string{}
		if err != nil || json.Unmarshal(result.Stdout, &names) != nil {
			return nil, domain.Err("CATALOG_UNAVAILABLE", "Configuration enumeration failed without builds or lock changes; provide .nixmox/configurations.json for another layout.", 503)
		}
		if len(names) > 1000 {
			return nil, domain.Err("STORAGE_LIMIT", "Configuration catalog exceeds 1000 entries.", 507)
		}
		for _, name := range names {
			descriptors = append(descriptors, descriptor{Configuration: render.Configuration{Name: name, FlakeDirectory: ".", FlakeAttribute: "nixosConfigurations." + name}})
		}
	}
	seen := map[string]bool{}
	for _, d := range descriptors {
		if !domain.SafeID.MatchString(d.Name) || seen[d.Name] || (d.FlakeDirectory != "." && !gitstore.SafePath(d.FlakeDirectory)) || d.FlakeAttribute == "" {
			return nil, domain.Err("INVALID_CATALOG", "Invalid or duplicate configuration descriptor.", 422)
		}
		seen[d.Name] = true
		rows = append(rows, domain.Configuration{SchemaVersion: 1, ID: gitstore.StableID("cfg_", r.ID+"/"+d.Name), Version: 1, RepositoryID: r.ID, Name: d.Name, FlakeDirectory: d.FlakeDirectory, FlakeAttribute: d.FlakeAttribute, RefID: ref, Readiness: "unvalidated", InspectedCommit: commit, PreparationTemplates: d.PreparationTemplates})
	}
	return rows, nil
}
func (s *Service) Configurations(ctx context.Context, id, ref, commit, limit, cursor string) (domain.Page[domain.Configuration], error) {
	if (ref != "" || commit != "") && id == "" {
		return domain.Page[domain.Configuration]{}, domain.Err("INVALID_QUERY", "Revision selectors require repository_id.", 400)
	}
	repos := []domain.Repository{}
	if id != "" {
		r, err := s.Sources.Repository(ctx, id)
		if err != nil {
			return domain.Page[domain.Configuration]{}, err
		}
		repos = append(repos, r)
	} else {
		next := ""
		for {
			page, err := s.Sources.Repositories(ctx, "500", next)
			if err != nil {
				return domain.Page[domain.Configuration]{}, err
			}
			repos = append(repos, page.Items...)
			if page.NextCursor == nil {
				break
			}
			next = *page.NextCursor
		}
	}
	rows := []domain.Configuration{}
	pins := map[string]string{}
	for _, r := range repos {
		_, selected, head, err := s.Sources.Source(ctx, r.ID, ref, commit)
		if err != nil {
			return domain.Page[domain.Configuration]{}, err
		}
		catalog, err := s.catalog(ctx, r, selected, head)
		if err != nil {
			return domain.Page[domain.Configuration]{}, err
		}
		pins[r.ID] = head
		if ref == "" && commit == "" {
			tracked := map[string]domain.Configuration{}
			for _, c := range r.Configurations {
				for _, v := range r.Refs {
					if c.RefID == v.ID {
						c.InspectedCommit = v.Head
					}
				}
				tracked[c.ID] = c
				pins[c.ID] = c.InspectedCommit
			}
			for i, c := range catalog {
				if v, ok := tracked[c.ID]; ok {
					catalog[i] = v
					delete(tracked, c.ID)
				}
			}
			for _, c := range tracked {
				catalog = append(catalog, c)
			}
		}
		rows = append(rows, catalog...)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return domain.Paginate(rows, struct {
		Repository, Ref, Commit string
		Pins                    map[string]string
	}{id, ref, commit, pins}, limit, cursor, func(c domain.Configuration) string { return c.ID })
}
func (s *Service) Configuration(ctx context.Context, id string) (domain.Configuration, error) {
	next := ""
	for {
		page, err := s.Configurations(ctx, "", "", "", "500", next)
		if err != nil {
			return domain.Configuration{}, err
		}
		for _, c := range page.Items {
			if c.ID == id {
				return c, nil
			}
		}
		if page.NextCursor == nil {
			break
		}
		next = *page.NextCursor
	}
	return domain.Configuration{}, domain.Err("NOT_FOUND", "Configuration not found.", 404)
}
