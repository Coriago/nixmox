package configuration

import (
	"context"
	"errors"

	"nixmox/internal/recipe"
)

// Render returns a bounded file preview, not paths to a discarded worktree.
// No observation is collected and no Git commit is created.
func (s Service) Render(ctx context.Context, id string, version uint64) (recipe.Rendered, error) {
	value, err := s.Get(ctx, id, version)
	if err != nil {
		return recipe.Rendered{}, err
	}
	recipeVersion, err := s.loadRecipe(ctx, value)
	if err != nil {
		return recipe.Rendered{}, err
	}
	if err = invalidateStaleInputs(ctx, &value, recipeVersion); err != nil {
		return recipe.Rendered{}, err
	}
	if !value.CanRender {
		return recipe.Rendered{}, errors.New("configuration has missing or stale inputs")
	}
	repo, err := s.Repositories.Get(ctx, value.TargetRepo.ID)
	if err != nil {
		return recipe.Rendered{}, err
	}
	work, err := s.Sources.Checkout(ctx, repo.Value, value.TargetRepo)
	if err != nil {
		return recipe.Rendered{}, err
	}
	defer work.Close()
	return recipe.Render(ctx, recipeVersion.SourceDefinition, templateInputs(value, recipeVersion), work)
}
