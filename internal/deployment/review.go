package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"nixmox/internal/host"
)

// Nix owns derivation-name parsing; do not invent package/version splitting.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type PackageChanges struct {
	Added   []Package `json:"added"`
	Removed []Package `json:"removed"`
}

func closurePackages(ctx context.Context, process host.Process, path string) (map[Package]bool, error) {
	data, err := process.Run(ctx, "", nil, "nix", "path-info", "--recursive", "--json", "--json-format", "1", path)
	if err != nil {
		return nil, err
	}
	var paths map[string]json.RawMessage
	if err = json.Unmarshal(data, &paths); err != nil {
		return nil, err
	}
	names := []string{}
	for path := range paths {
		base := filepath.Base(path)
		if len(base) < 34 {
			return nil, errors.New("invalid closure path")
		}
		names = append(names, base[33:])
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return nil, err
	}
	// JSON text is itself a Nix string via a second encoding (no expression injection).
	quoted, _ := json.Marshal(string(encoded))
	expression := "map builtins.parseDrvName (builtins.fromJSON " + strings.ReplaceAll(string(quoted), "${", "\\${") + ")"
	data, err = process.Run(ctx, "", nil, "nix", "eval", "--json", "--expr", expression)
	if err != nil {
		return nil, err
	}
	var packages []Package
	if err = json.Unmarshal(data, &packages); err != nil {
		return nil, err
	}
	result := map[Package]bool{}
	for _, pkg := range packages {
		result[pkg] = true
	}
	return result, nil
}

func packageChanges(ctx context.Context, process host.Process, current, proposed string) (PackageChanges, error) {
	result := PackageChanges{Added: []Package{}, Removed: []Package{}}
	before, err := closurePackages(ctx, process, current)
	if err != nil {
		return result, err
	}
	after, err := closurePackages(ctx, process, proposed)
	if err != nil {
		return result, err
	}
	for pkg := range after {
		if !before[pkg] {
			result.Added = append(result.Added, pkg)
		}
	}
	for pkg := range before {
		if !after[pkg] {
			result.Removed = append(result.Removed, pkg)
		}
	}
	less := func(values []Package) {
		sort.Slice(values, func(i, j int) bool {
			if values[i].Name != values[j].Name {
				return values[i].Name < values[j].Name
			}
			return values[i].Version < values[j].Version
		})
	}
	less(result.Added)
	less(result.Removed)
	return result, nil
}
