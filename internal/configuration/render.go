package configuration

import (
	"context"
	"errors"

	"nixmox/internal/recipe"
	"nixmox/internal/source"
)

// Render returns a bounded preview. It neither collects observations nor
// publishes a commit; the private pinned worktree is discarded after rendering.
func (s Service) Render(ctx context.Context, id string, version uint64) (recipe.Rendered, error) {
	value, err := s.Get(ctx, id, version)
	if err != nil {
		return recipe.Rendered{}, err
	}
	if value.Recipe == nil {
		return recipe.Rendered{}, recipe.Invalid(errors.New("configuration has no recipe"))
	}
	work, rendered, err := s.materializeVersion(ctx, value)
	if err != nil {
		return recipe.Rendered{}, err
	}
	defer work.Close()
	return rendered, nil
}

// Materialize uses the same render as preview, but transfers worktree ownership
// to the caller. Deployment and future publication must not refresh pins or
// recollect observations while consuming an immutable configuration version.
func (s Service) Materialize(ctx context.Context, id string, version uint64) (*source.Worktree, string, string, error) {
	value, err := s.Get(ctx, id, version)
	if err != nil {
		return nil, "", "", err
	}
	work, rendered, err := s.materializeVersion(ctx, value)
	if err != nil {
		return nil, "", "", err
	}
	directory, err := work.Directory(rendered.FlakeDir)
	if err != nil {
		work.Close()
		return nil, "", "", err
	}
	return work, directory, rendered.FlakeConfiguration, nil
}

func (s Service) materializeVersion(ctx context.Context, value Version) (*source.Worktree, recipe.Rendered, error) {
	var definition recipe.Version
	rendered := recipe.Rendered{FlakeDir: value.FlakeDir, FlakeConfiguration: value.FlakeConfiguration}
	if value.Recipe != nil {
		var err error
		definition, err = s.loadRecipe(ctx, value)
		if err != nil {
			return nil, rendered, err
		}
		if err = invalidateStaleInputs(ctx, &value, definition); err != nil {
			return nil, rendered, err
		}
		if !value.CanRender {
			return nil, rendered, errors.New("configuration has missing or stale inputs")
		}
	}
	repo, err := s.Repositories.Get(ctx, value.TargetRepo.ID)
	if err != nil {
		return nil, rendered, err
	}
	work, err := s.Sources.Checkout(ctx, repo.Value, value.TargetRepo)
	if err != nil {
		return nil, rendered, err
	}
	if value.Recipe != nil {
		rendered, err = recipe.Render(ctx, definition.SourceDefinition, templateInputs(value, definition), work)
		if err != nil {
			work.Close()
			return nil, rendered, err
		}
	}
	return work, rendered, nil
}
