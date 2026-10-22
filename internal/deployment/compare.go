package deployment

import (
	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/nixstore"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Nix names split at the first hyphen followed by a digit. A name without a
// version, or multiple remaining outputs of a name, is deliberately unpaired.
func packageName(path string) (string, *string) {
	name := filepath.Base(path)[33:]
	for i := 0; i+1 < len(name); i++ {
		if name[i] == '-' && unicode.IsDigit(rune(name[i+1])) {
			v := name[i+1:]
			return name[:i], &v
		}
	}
	return name, nil
}
func compareClosures(before, after []nixstore.Output) ([]domain.PackageChange, domain.DiffSummary, domain.ClosureSize) {
	summary := domain.DiffSummary{}
	oldPaths := map[string]nixstore.Output{}
	newPaths := map[string]nixstore.Output{}
	var beforeSize, afterSize int64
	for _, v := range before {
		oldPaths[v.Path] = v
		beforeSize += v.NarSize
	}
	for _, v := range after {
		newPaths[v.Path] = v
		afterSize += v.NarSize
	}
	oldGroups := map[string][]domain.PackageOutput{}
	newGroups := map[string][]domain.PackageOutput{}
	for _, v := range before {
		if n, ok := newPaths[v.Path]; ok && n.NarHash == v.NarHash {
			summary.Unchanged++
			continue
		}
		name, version := packageName(v.Path)
		oldGroups[name] = append(oldGroups[name], domain.PackageOutput{Version: version, StorePath: v.Path})
	}
	for _, v := range after {
		if o, ok := oldPaths[v.Path]; ok && o.NarHash == v.NarHash {
			continue
		}
		name, version := packageName(v.Path)
		newGroups[name] = append(newGroups[name], domain.PackageOutput{Version: version, StorePath: v.Path})
	}
	names := map[string]bool{}
	for name := range oldGroups {
		names[name] = true
	}
	for name := range newGroups {
		names[name] = true
	}
	changes := []domain.PackageChange{}
	for name := range names {
		o, n := oldGroups[name], newGroups[name]
		if o == nil {
			o = []domain.PackageOutput{}
		}
		if n == nil {
			n = []domain.PackageOutput{}
		}
		sort.Slice(o, func(i, j int) bool { return o[i].StorePath < o[j].StorePath })
		sort.Slice(n, func(i, j int) bool { return n[i].StorePath < n[j].StorePath })
		kind := "ambiguous"
		known := true
		for _, v := range append(append([]domain.PackageOutput{}, o...), n...) {
			if v.Version == nil {
				known = false
			}
		}
		switch {
		case !known || len(o) > 1 || len(n) > 1:
			summary.Ambiguous++
		case len(o) == 0:
			kind = "added"
			summary.Added++
		case len(n) == 0:
			kind = "removed"
			summary.Removed++
		case *o[0].Version == *n[0].Version:
			kind = "output_changed"
			summary.OutputChanged++
		default:
			kind = "version_changed"
			summary.VersionChanged++
		}
		changes = append(changes, domain.PackageChange{ID: gitstore.StableID("change_", name), Kind: kind, Name: name, Old: o, New: n})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	delta := afterSize - beforeSize
	return changes, summary, domain.ClosureSize{BeforeBytes: &beforeSize, AfterBytes: &afterSize, DeltaBytes: &delta}
}
func matchesChange(c domain.PackageChange, kind, q string) bool {
	if kind != "" && c.Kind != kind {
		return false
	}
	q = strings.ToLower(q)
	if strings.Contains(strings.ToLower(c.Name), q) {
		return true
	}
	for _, group := range [][]domain.PackageOutput{c.Old, c.New} {
		for _, v := range group {
			if strings.Contains(strings.ToLower(v.StorePath), q) || v.Version != nil && strings.Contains(strings.ToLower(*v.Version), q) {
				return true
			}
		}
	}
	return false
}
