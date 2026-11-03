package configuration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"

	"nixmox/internal/cluster"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
)

type Observer interface {
	Collect(context.Context, recipe.Observation) (ObservationResult, error)
}

type Service struct {
	Repository
	Recipes      recipe.Repository
	Observer     Observer
	Sources      source.Manager
	Repositories cluster.Store[source.Repository]
}

func (s Service) Create(ctx context.Context, input Create) (Version, error) {
	input, err := recipe.Canonical(input)
	if err != nil {
		return Version{}, err
	}
	value := Version{
		ID:                 uuid.NewString(),
		Version:            1,
		Name:               input.Name,
		Recipe:             input.Recipe,
		TargetRepo:         input.TargetRepo,
		FlakeDir:           input.FlakeDir,
		FlakeConfiguration: input.FlakeConfiguration,
		Collected:          Collected{Fields: input.Fields},
	}
	if err = s.validateTarget(value); err != nil {
		return Version{}, err
	}
	if value.Recipe == nil {
		if len(input.Fields) > 0 {
			return Version{}, recipe.Invalid(errors.New("Git configuration cannot collect recipe inputs"))
		}
		return s.save(ctx, value)
	}
	recipeVersion, err := s.loadRecipe(ctx, value)
	if err != nil {
		return Version{}, err
	}
	if err = invalidateStaleInputs(ctx, &value, recipeVersion); err != nil {
		return Version{}, err
	}
	if err = validateSubmitted(ctx, value, recipeVersion, input.Fields); err != nil {
		return Version{}, err
	}
	return s.save(ctx, value)
}

func (s Service) SetInputs(ctx context.Context, id string, expected uint64, fields map[string]any) (Version, error) {
	value, err := s.current(ctx, id, expected)
	if err != nil {
		return Version{}, err
	}
	recipeVersion, err := s.loadRecipe(ctx, value)
	if err != nil {
		return Version{}, err
	}
	fields, err = recipe.Canonical(fields)
	if err != nil {
		return Version{}, err
	}
	if value.Collected.Fields == nil {
		value.Collected.Fields = map[string]any{}
	}
	for name, input := range fields {
		if _, ok := recipeVersion.SourceDefinition.Fields[name]; !ok {
			return Version{}, recipe.Invalid(fmt.Errorf("unknown field %q", name))
		}
		if input == nil {
			delete(value.Collected.Fields, name)
		} else {
			value.Collected.Fields[name] = input
		}
	}
	if err = invalidateStaleInputs(ctx, &value, recipeVersion); err != nil {
		return Version{}, err
	}
	if err = validateSubmitted(ctx, value, recipeVersion, fields); err != nil {
		return Version{}, err
	}
	value.Version++
	return s.save(ctx, value)
}

func (s Service) UpgradeRecipe(ctx context.Context, id string, expected uint64, reference recipe.Reference) (Version, error) {
	value, err := s.current(ctx, id, expected)
	if err != nil {
		return Version{}, err
	}
	if value.Recipe == nil {
		return Version{}, recipe.Invalid(errors.New("Git configuration has no recipe to upgrade"))
	}
	value.Recipe = &reference
	recipeVersion, err := s.loadRecipe(ctx, value)
	if err != nil {
		return Version{}, err
	}
	if err = invalidateStaleInputs(ctx, &value, recipeVersion); err != nil {
		return Version{}, err
	}
	value.Version++
	return s.save(ctx, value)
}

func (s Service) RefreshTarget(ctx context.Context, id string, expected uint64, pin source.Pin) (Version, error) {
	value, err := s.current(ctx, id, expected)
	if err != nil {
		return Version{}, err
	}
	if err = pin.Validate(); err != nil {
		return Version{}, recipe.Invalid(err)
	}
	value.TargetRepo = pin
	if value.Recipe != nil {
		recipeVersion, err := s.loadRecipe(ctx, value)
		if err != nil {
			return Version{}, err
		}
		if err = invalidateStaleInputs(ctx, &value, recipeVersion); err != nil {
			return Version{}, err
		}
	}
	value.Version++
	return s.save(ctx, value)
}

func (s Service) loadRecipe(ctx context.Context, value Version) (recipe.Version, error) {
	if value.Recipe == nil {
		return recipe.Version{}, recipe.Invalid(errors.New("configuration has no recipe"))
	}
	return s.Recipes.Get(ctx, value.Recipe.ID, value.Recipe.Version)
}

func (s Service) validateTarget(value Version) error {
	if err := value.TargetRepo.Validate(); err != nil {
		return recipe.Invalid(err)
	}
	if value.Recipe == nil && (!filepath.IsLocal(value.FlakeDir) || value.FlakeConfiguration == "") {
		return recipe.Invalid(errors.New("Git configuration requires local flake directory and configuration name"))
	}
	if value.Recipe != nil && (value.FlakeDir != "" || value.FlakeConfiguration != "") {
		return recipe.Invalid(errors.New("recipe defines flake location; do not override it on the configuration"))
	}
	return nil
}
