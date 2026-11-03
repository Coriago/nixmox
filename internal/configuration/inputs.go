package configuration

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"nixmox/internal/recipe"
)

func templateInputs(value Version, recipeVersion recipe.Version) map[string]any {
	result := maps.Clone(value.Collected.Fields)
	if result == nil {
		result = map[string]any{}
	}
	observations := map[string]any{}
	for name, observation := range value.Collected.Observations {
		observations[name] = observation.Output
	}
	result["observations"] = observations
	// Pins are already serializable; normalize to the lowercase JSON template keys.
	pin, _ := recipe.Object(value.TargetRepo)
	result["targetRepo"] = pin
	for name, value := range recipeVersion.SourceInputs() {
		pinned, _ := recipe.Object(value)
		result[name] = pinned
	}
	return result
}

func dependenciesReady(value Version, input recipe.Input) bool {
	for _, dependency := range input.Depends {
		if dependency.Observation {
			if _, ok := value.Collected.Observations[dependency.Name]; !ok {
				return false
			}
		} else {
			if _, ok := value.Collected.Fields[dependency.Name]; !ok {
				return false
			}
		}
	}
	return true
}

// invalidateStaleInputs invalidates previously collected values, never validates a new user
// submission. Callers validate submitted values separately so errors are explicit.
func invalidateStaleInputs(ctx context.Context, value *Version, recipeVersion recipe.Version) error {
	order, err := recipeVersion.SourceDefinition.Order()
	if err != nil {
		return err
	}
	if value.Collected.Fields == nil {
		value.Collected.Fields = map[string]any{}
	}
	if value.Collected.Observations == nil {
		value.Collected.Observations = map[string]ObservationResult{}
	}

	// Remove inputs no longer declared by the selected recipe version.
	for name := range value.Collected.Fields {
		if _, ok := recipeVersion.SourceDefinition.Fields[name]; !ok {
			delete(value.Collected.Fields, name)
		}
	}
	for name := range value.Collected.Observations {
		if _, ok := recipeVersion.SourceDefinition.Observations[name]; !ok {
			delete(value.Collected.Observations, name)
		}
	}

	// Visit dependencies first so invalidation propagates to their consumers.
	for _, input := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		inputs := templateInputs(*value, recipeVersion)
		if input.Observation {
			observed, ok := value.Collected.Observations[input.Name]
			if !ok {
				continue
			}
			observation := recipeVersion.SourceDefinition.Observations[input.Name]
			_, hash, err := resolveObservation(ctx, observation, inputs)
			if !dependenciesReady(*value, input) || err != nil || hash != observed.InputHash {
				delete(value.Collected.Observations, input.Name)
			}
			continue
		}

		current, exists := value.Collected.Fields[input.Name]
		if !exists {
			continue
		}
		field := recipeVersion.SourceDefinition.Fields[input.Name]
		if !dependenciesReady(*value, input) || field.Validate(ctx, current, inputs) != nil {
			delete(value.Collected.Fields, input.Name)
		}
	}
	fieldsComplete := len(value.Collected.Fields) == len(recipeVersion.SourceDefinition.Fields)
	observationsComplete := len(value.Collected.Observations) == len(recipeVersion.SourceDefinition.Observations)
	value.CanRender = fieldsComplete && observationsComplete
	return nil
}

func validateSubmitted(ctx context.Context, value Version, recipeVersion recipe.Version, fields map[string]any) error {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		field, ok := recipeVersion.SourceDefinition.Fields[name]
		if !ok {
			return recipe.Invalid(fmt.Errorf("unknown field %q", name))
		}
		if fields[name] == nil {
			continue
		}
		if err := field.Validate(ctx, fields[name], templateInputs(value, recipeVersion)); err != nil {
			return fmt.Errorf("field %s: %w", name, err)
		}
		if _, ok := value.Collected.Fields[name]; !ok {
			return recipe.Invalid(fmt.Errorf("field %s dependencies are incomplete", name))
		}
	}
	return nil
}
