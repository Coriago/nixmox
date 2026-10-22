package configuration

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"nixmox/internal/gitstore"
	"nixmox/internal/process"
	"nixmox/internal/render"
)

func sortFiles(files []render.File) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}
func (s *Service) validate(ctx context.Context, d Draft) (Validation, error) {
	v := Validation{ID: gitstore.StableID("validation_", d.ID+"/"+string(mustJSON(d.Files))), DraftVersion: d.Version, State: "ready", Missing: []string{}, Errors: []string{}}
	dir, err := s.Sources.Git.Worktree(ctx, d.RepositoryID, d.BaseCommit, v.ID)
	if err != nil {
		return v, err
	}
	for _, f := range d.Files {
		if err = gitstore.WriteFile(dir, f.Path, []byte(f.Content)); err != nil {
			return v, err
		}
		switch f.Format {
		case "json":
			if !json.Valid([]byte(f.Content)) {
				v.Errors = append(v.Errors, "Invalid JSON: "+f.Path)
			}
		case "nix":
			if _, err = process.Run(ctx, process.Options{Timeout: 15 * time.Second, MaxOutput: 1 << 20}, "nix-instantiate", "--parse", filepath.Join(dir, f.Path)); err != nil {
				v.Errors = append(v.Errors, "Invalid Nix syntax: "+f.Path)
			}
		}
	}
	if len(v.Errors) > 0 {
		v.State = "invalid"
		return v, nil
	}
	flakeDir := filepath.Join(dir, d.Configuration.FlakeDirectory)
	// Lightweight validation cannot update flake.lock, import from derivations,
	// accept repository Nix settings, or realize a system output.
	args := []string{"eval", "--json", "--no-write-lock-file", "--no-update-lock-file", "--option", "allow-import-from-derivation", "false", "--option", "accept-flake-config", "false", "path:" + flakeDir + "#" + d.Configuration.FlakeAttribute, "--apply", "builtins.isAttrs"}
	r, err := process.Run(ctx, process.Options{Timeout: time.Minute, MaxOutput: 1 << 20}, "nix", args...)
	if err != nil || strings.TrimSpace(string(r.Stdout)) != "true" {
		v.State = "invalid"
		v.Errors = append(v.Errors, "Declared configuration export could not be validated without builds or lock updates.")
		return v, nil
	}
	args[len(args)-3] = "path:" + flakeDir + "#" + d.Configuration.FlakeAttribute + ".config.system.build.toplevel.drvPath"
	args = args[:len(args)-2]
	r, err = process.Run(ctx, process.Options{Timeout: time.Minute, MaxOutput: 1 << 20}, "nix", args...)
	if err != nil {
		// Missing installation prerequisites are saveable. Other evaluation errors
		// retain an invalid draft rather than labeling unverified source ready.
		message := string(r.Stderr)
		if strings.Contains(message, "fileSystems") || strings.Contains(message, "boot.loader") || strings.Contains(message, "hostPlatform") || strings.Contains(message, "hardware-configuration.nix") {
			v.State = "incomplete"
			v.Missing = []string{"hardware_or_boot_configuration"}
		} else {
			v.State = "invalid"
			v.Errors = []string{"System evaluation failed; lightweight validation disables import-from-derivation."}
		}
	}
	return v, nil
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
