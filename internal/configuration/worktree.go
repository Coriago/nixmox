package configuration

import (
	"context"
	"errors"

	"nixmox/internal/recipe"
	"nixmox/internal/source"
)

// Materialize owns a fresh pinned worktree until the caller closes it. Recipe
// inputs must already be collected; deployment never refreshes pins or observes.
func (s Service) Materialize(ctx context.Context, id string, version uint64) (*source.Worktree, string, string, error) {
	value, err := s.Get(ctx, id, version)
	if err != nil {
		return nil, "", "", err
	}
	repo, err := s.Repositories.Get(ctx, value.TargetRepo.ID)
	if err != nil {
		return nil, "", "", err
	}
	work, err := s.Sources.Checkout(ctx, repo.Value, value.TargetRepo)
	if err != nil {
		return nil, "", "", err
	}
	directory, name := value.FlakeDir, value.FlakeConfiguration
	if value.Recipe != nil {
		definition, loadErr := s.loadRecipe(ctx, value)
		err = loadErr
		if err == nil {
			err = invalidateStaleInputs(ctx, &value, definition)
		}
		if err == nil && !value.CanRender {
			err = errors.New("configuration has missing or stale inputs")
		}
		if err == nil {
			var rendered recipe.Rendered
			rendered, err = recipe.Render(ctx, definition.SourceDefinition, templateInputs(value, definition), work)
			directory, name = rendered.FlakeDir, rendered.FlakeConfiguration
		}
	}
	if err != nil {
		work.Close()
		return nil, "", "", err
	}
	directory, err = work.Directory(directory)
	if err != nil {
		work.Close()
		return nil, "", "", err
	}
	return work, directory, name, nil
}
