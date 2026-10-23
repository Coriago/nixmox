package nix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"nixmox/internal/domain"
)

// CheckConfiguration verifies the selected exported attribute without evaluating
// a derivation or building the system.
func (n Engine) CheckConfiguration(ctx context.Context, source domain.Source, worktree string) error {
	data, err := n.evaluate(ctx, source, worktree, true, "builtins.isAttrs", 1024)
	if err != nil || strings.TrimSpace(string(data)) != "true" {
		return &domain.Error{Code: "CONFIGURATION_UNAVAILABLE", Message: "Selected configuration attribute is unavailable at the pinned commit"}
	}
	return nil
}

// Configurations inspects only the supplied checkout. Reads never fetch or update
// locks, and enumeration cannot trigger import-from-derivation builds.
func (n Engine) Configurations(ctx context.Context, source domain.Source, worktree string) ([]domain.ConfigurationDescriptor, error) {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(".nixmox/configurations.json")
	var items []domain.ConfigurationDescriptor
	if err == nil {
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, domain.CommandPayloadLimit+1))
		if err != nil {
			return nil, err
		}
		if len(data) > domain.CommandPayloadLimit {
			return nil, errors.New("configuration catalog exceeds size limit")
		}
		var catalog domain.ConfigurationCatalog
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&catalog); err != nil {
			return nil, err
		}
		if decoder.Decode(new(any)) != io.EOF || catalog.SchemaVersion != domain.SchemaVersion {
			return nil, errors.New("invalid configuration catalog schema")
		}
		items = catalog.Configurations
	} else if errors.Is(err, os.ErrNotExist) {
		source.FlakeDirectory, source.FlakeAttribute = ".", "nixosConfigurations"
		data, err := n.evaluate(ctx, source, worktree, true, "builtins.attrNames", domain.CommandPayloadLimit)
		if err != nil {
			return nil, err
		}
		var names []string
		if err = json.Unmarshal(data, &names); err != nil {
			return nil, err
		}
		for _, name := range names {
			// JSON quoting also produces a Nix attribute string once interpolation is escaped.
			quoted, _ := json.Marshal(name)
			items = append(items, domain.ConfigurationDescriptor{Name: name, FlakeDirectory: ".", FlakeAttribute: "nixosConfigurations." + strings.ReplaceAll(string(quoted), "${", "\\${")})
		}
	} else {
		return nil, err
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.Name == "" || len(item.Name) > 128 || strings.ContainsAny(item.Name, "\x00\n\r") || seen[item.Name] {
			return nil, errors.New("invalid or duplicate configuration name")
		}
		seen[item.Name] = true
		dir := item.FlakeDirectory
		if dir == "" || filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == ".." || strings.HasPrefix(dir, "../") || strings.ContainsAny(dir, "\\\x00") || item.FlakeAttribute == "" || strings.ContainsAny(item.FlakeAttribute, "\x00\n\r") {
			return nil, errors.New("invalid configuration descriptor")
		}
		// OpenRoot prevents descriptor symlinks from escaping the checkout.
		f, err := root.Open(filepath.Join(dir, "flake.nix"))
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	slices.SortFunc(items, func(a, b domain.ConfigurationDescriptor) int { return strings.Compare(a.Name, b.Name) })
	if items == nil {
		items = []domain.ConfigurationDescriptor{}
	}
	return items, nil
}
