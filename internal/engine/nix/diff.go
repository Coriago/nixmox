package nix

import (
	"context"
	"encoding/json"
	"math"
	"sort"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

type ClosureDiffer struct{}

func (ClosureDiffer) Compare(ctx context.Context, before, after []domain.ClosurePath) (domain.PackageDiff, error) {
	result := domain.PackageDiff{Record: domain.Record{SchemaVersion: 1, Version: 1}, Scope: "system_closure", State: "ready", Changes: []domain.PackageChange{}}
	old, newer := map[string]domain.ClosurePath{}, map[string]domain.ClosurePath{}
	for _, p := range before {
		old[p.StorePath] = p
	}
	for _, p := range after {
		newer[p.StorePath] = p
	}
	result.ClosureSize.BeforeBytes = closureSize(old)
	result.ClosureSize.AfterBytes = closureSize(newer)
	if a, b := result.ClosureSize.BeforeBytes, result.ClosureSize.AfterBytes; a != nil && b != nil && *a <= math.MaxInt64 && *b <= math.MaxInt64 {
		result.ClosureSize.DeltaBytes = shared.Ptr(int64(*b) - int64(*a))
	}
	groups := map[string]*domain.PackageChange{}
	add := func(p domain.ClosurePath, previous bool) {
		key := "unversioned:" + p.StorePath
		name := p.StorePath
		if p.Name != nil {
			name = *p.Name
			key = "named:" + name
		}
		g := groups[key]
		if g == nil {
			g = &domain.PackageChange{Name: name, Old: []domain.PackageOutput{}, New: []domain.PackageOutput{}}
			groups[key] = g
		}
		output := domain.PackageOutput{Version: p.Version, StorePath: p.StorePath}
		if previous {
			g.Old = append(g.Old, output)
		} else {
			g.New = append(g.New, output)
		}
	}
	for path, p := range old {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, same := newer[path]; same {
			result.Summary.Unchanged++
			continue
		}
		add(p, true)
	}
	for path, p := range newer {
		if _, same := old[path]; !same {
			add(p, false)
		}
	}
	for key, g := range groups {
		sort.Slice(g.Old, func(i, j int) bool { return g.Old[i].StorePath < g.Old[j].StorePath })
		sort.Slice(g.New, func(i, j int) bool { return g.New[i].StorePath < g.New[j].StorePath })
		switch {
		case len(key) >= 12 && key[:12] == "unversioned:":
			g.Kind = "ambiguous"
			result.Summary.Ambiguous++
		case len(g.Old) == 0:
			g.Kind = "added"
			result.Summary.Added++
		case len(g.New) == 0:
			g.Kind = "removed"
			result.Summary.Removed++
		case len(g.Old) != 1 || len(g.New) != 1 || g.Old[0].Version == nil || g.New[0].Version == nil:
			g.Kind = "ambiguous"
			result.Summary.Ambiguous++
		case *g.Old[0].Version == *g.New[0].Version:
			g.Kind = "output_changed"
			result.Summary.OutputChanged++
		default:
			g.Kind = "version_changed"
			result.Summary.VersionChanged++
		}
		raw, _ := json.Marshal(g)
		g.ID = shared.ID("change_", domain.ID(raw))
		result.Changes = append(result.Changes, *g)
	}
	sort.Slice(result.Changes, func(i, j int) bool { return result.Changes[i].ID < result.Changes[j].ID })
	identity, _ := json.Marshal([]any{old, newer})
	result.ID = shared.ID("diff_", domain.ID(identity))
	return result, nil
}

func closureSize(paths map[string]domain.ClosurePath) *uint64 {
	var size uint64
	for _, p := range paths {
		if p.NARSize == nil || *p.NARSize > math.MaxUint64-size {
			return nil
		}
		size += *p.NARSize
	}
	return &size
}
