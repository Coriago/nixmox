package credentials

import (
	"context"
	"encoding/json"
	"nixmox/internal/domain"
	"slices"
	"sort"
)

// Claim the dependency before publishing a repository/settings binding. A
// partially completed binding may retain a conservative pin; removal cannot
// race a successful new reference and erase its credential.
func (v *Vault) Bind(ctx context.Context, ref, kind, resource string) error {
	for attempt := 0; attempt < 4; attempt++ {
		c, err := v.Credential(ctx, ref)
		if err != nil {
			return err
		}
		var current domain.Credential
		rev, err := v.State.Get(ctx, "credential."+c.ID, &current)
		if err != nil {
			return err
		}
		c = current
		if c.State != "available" {
			return domain.Err("CREDENTIAL_UNAVAILABLE", "Credential is being removed or is unavailable.", 503)
		}
		var target *[]string
		switch kind {
		case "repository":
			if c.Kind != "git-http-token" && c.Kind != "git-ssh-key" {
				return domain.Err("CREDENTIAL_KIND", "Repository requires a Git credential.", 422)
			}
			target = &c.Scope.RepositoryIDs
		case "cache":
			if c.Kind != "nix-cache-signing-key" {
				return domain.Err("CREDENTIAL_KIND", "Cache requires a Nix signing key.", 422)
			}
			target = &c.Scope.CacheNodeIDs
		case "updater":
			if c.Kind != "sops-age-key" {
				return domain.Err("CREDENTIAL_KIND", "SOPS requires an age key.", 422)
			}
			target = &c.Scope.UpdaterNodeIDs
		default:
			return domain.Err("INVALID_STATE", "Unknown credential binding.", 422)
		}
		if slices.Contains(*target, resource) {
			return nil
		}
		*target = append(*target, resource)
		sort.Strings(*target)
		c.Version++
		if _, err = v.State.CAS(ctx, "credential."+c.ID, c, rev); err == nil {
			return nil
		}
		var conflict *domain.Error
		if !errorsAsConflict(err, &conflict) {
			return err
		}
	}
	return domain.Err("VERSION_CONFLICT", "Credential changed while binding.", 409)
}
func (v *Vault) repositoryReferences(ctx context.Context, c domain.Credential) (bool, error) {
	rows, err := v.State.List(ctx, "repository.")
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		var r domain.Repository
		if json.Unmarshal(row.Data, &r) != nil {
			return false, unavailable()
		}
		if r.Remote.CredentialRef != nil && (*r.Remote.CredentialRef == c.ID || *r.Remote.CredentialRef == c.Name) {
			return true, nil
		}
	}
	return false, nil
}

// Call while holding the resource's command exclusion, after publishing its
// replacement binding. CAS preserves concurrent bindings of other resources.
func (v *Vault) PruneBinding(ctx context.Context, selected *string, kind, resource string) error {
	keep := ""
	if selected != nil {
		credential, err := v.Credential(ctx, *selected)
		if err != nil {
			return err
		}
		keep = credential.ID
	}
	rows, err := v.State.List(ctx, "credential.")
	if err != nil {
		return err
	}
	for _, row := range rows {
		var c domain.Credential
		if json.Unmarshal(row.Data, &c) != nil {
			return unavailable()
		}
		if c.ID == keep {
			continue
		}
		for attempt := 0; attempt < 4; attempt++ {
			rev, err := v.State.Get(ctx, "credential."+c.ID, &c)
			if err != nil {
				return err
			}
			var target *[]string
			switch kind {
			case "repository":
				target = &c.Scope.RepositoryIDs
			case "cache":
				target = &c.Scope.CacheNodeIDs
			case "updater":
				target = &c.Scope.UpdaterNodeIDs
			default:
				return domain.Err("INVALID_STATE", "Unknown credential binding.", 422)
			}
			if !slices.Contains(*target, resource) {
				break
			}
			*target = slices.DeleteFunc(*target, func(id string) bool { return id == resource })
			c.Version++
			if _, err = v.State.CAS(ctx, "credential."+c.ID, c, rev); err == nil {
				break
			}
			var conflict *domain.Error
			if !errorsAsConflict(err, &conflict) || attempt == 3 {
				return err
			}
		}
	}
	return nil
}
