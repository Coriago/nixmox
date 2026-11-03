package configuration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"nixmox/internal/recipe"
)

// Observe collects in dependency order and persists a new configuration version.
// Collector performs external operations; this method owns evidence and invalidation.
func (s Service) Observe(ctx context.Context, id string, expected uint64, name string) (Version, error) {
	if s.Observer == nil {
		return Version{}, errors.New("observation service unavailable")
	}
	value, err := s.current(ctx, id, expected)
	if err != nil {
		return Version{}, err
	}
	recipeVersion, err := s.loadRecipe(ctx, value)
	if err != nil {
		return Version{}, err
	}
	if err = invalidateStaleInputs(ctx, &value, recipeVersion); err != nil {
		return Version{}, err
	}
	if name != "" {
		if _, ok := recipeVersion.SourceDefinition.Observations[name]; !ok {
			return Version{}, recipe.Invalid(errors.New("unknown observation"))
		}
	}
	order, err := recipeVersion.SourceDefinition.Order()
	if err != nil {
		return Version{}, err
	}
	for _, input := range order {
		if !input.Observation || (name != "" && name != input.Name) {
			continue
		}
		if !dependenciesReady(value, input) {
			if name != "" {
				return Version{}, recipe.Invalid(errors.New("observation dependencies are incomplete"))
			}
			continue
		}
		observation := recipeVersion.SourceDefinition.Observations[input.Name]
		inputs := templateInputs(value, recipeVersion)
		resolved, hash, err := resolveObservation(ctx, observation, inputs)
		if err != nil {
			return Version{}, err
		}
		observed, err := s.Observer.Collect(ctx, resolved)
		if err != nil {
			return Version{}, fmt.Errorf("observe %s: %w", input.Name, err)
		}
		observed, err = recipe.Canonical(observed)
		if err != nil {
			return Version{}, err
		}
		observed.InputHash = hash
		observed.CollectedAt = time.Now().UTC()
		value.Collected.Observations[input.Name] = observed
		if err = invalidateStaleInputs(ctx, &value, recipeVersion); err != nil {
			return Version{}, err
		}
	}
	value.Version++
	return s.save(ctx, value)
}

func resolveObservation(ctx context.Context, spec recipe.Observation, inputs map[string]any) (recipe.Observation, string, error) {
	raw, err := recipe.Object(spec)
	if err != nil {
		return recipe.Observation{}, "", err
	}
	resolved, err := recipe.Evaluate(ctx, raw, inputs)
	if err != nil {
		return recipe.Observation{}, "", err
	}
	data, err := json.Marshal(resolved)
	if err != nil {
		return recipe.Observation{}, "", err
	}
	var request recipe.Observation
	if err = json.Unmarshal(data, &request); err != nil {
		return request, "", err
	}
	fingerprint, err := json.Marshal(struct {
		Definition recipe.Observation
		Resolved   recipe.Observation
	}{
		Definition: spec,
		Resolved:   request,
	})
	if err != nil {
		return request, "", err
	}
	hash := sha256.Sum256(fingerprint)
	return request, hex.EncodeToString(hash[:]), nil
}
