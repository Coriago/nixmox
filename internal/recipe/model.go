// Package recipe preserves versioned inputs and renders into pinned worktrees.
package recipe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"nixmox/internal/source"
)

type Field struct {
	Type        string `json:"type" yaml:"type"`
	Values      any    `json:"values,omitempty" yaml:"values,omitempty"`
	OptionValue string `json:"optionValue,omitempty" yaml:"optionValue,omitempty"`
	OptionLabel string `json:"optionLabel,omitempty" yaml:"optionLabel,omitempty"`
}
type Observation struct {
	Type          string `json:"type" yaml:"type"`
	Repo          any    `json:"repo,omitempty" yaml:"repo,omitempty"`
	Host          string `json:"host,omitempty" yaml:"host,omitempty"`
	Cmd           string `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	Path          string `json:"path,omitempty" yaml:"path,omitempty"`
	NoFileSystems bool   `json:"noFileSystems,omitempty" yaml:"noFileSystems,omitempty"`
}
type SOPSUpdate struct {
	Key         string   `json:"key" yaml:"key"`
	AnchorValue string   `json:"anchorValue" yaml:"anchorValue"`
	Rules       []string `json:"rules" yaml:"rules"`
}
type Output struct {
	Template   *string     `json:"template,omitempty" yaml:"template,omitempty"`
	Raw        *string     `json:"raw,omitempty" yaml:"raw,omitempty"`
	UpdateSOPS *SOPSUpdate `json:"updateSops,omitempty" yaml:"updateSops,omitempty"`
}
type Definition struct {
	FlakeDir           string                 `json:"flakeDir" yaml:"flakeDir"`
	FlakeConfiguration string                 `json:"flakeConfiguration" yaml:"flakeConfiguration"`
	Fields             map[string]Field       `json:"fields" yaml:"fields"`
	Observations       map[string]Observation `json:"observations" yaml:"observations"`
	Outputs            map[string]Output      `json:"outputs" yaml:"outputs"`
}
type Source struct {
	source.Pin
	Path string `json:"path"`
}
type CollectedObservation struct {
	CollectedAt time.Time   `json:"collectedAt"`
	Repo        *source.Pin `json:"repo,omitempty"`
	Host        string      `json:"host,omitempty"`
	Path        string      `json:"path,omitempty"`
	Output      any         `json:"output"`
	InputHash   string      `json:"inputHash"`
}
type Collected struct {
	Fields       map[string]any                  `json:"fields"`
	Observations map[string]CollectedObservation `json:"observations"`
}
type Record struct {
	ID               string     `json:"id"`
	Version          uint64     `json:"version"`
	CanRender        bool       `json:"canRender"`
	TargetRepo       source.Pin `json:"targetRepo"`
	RecipeSource     *Source    `json:"recipeSource,omitempty"`
	SourceDefinition Definition `json:"sourceDefinition"`
	Collected        Collected  `json:"collected"`
}

func clone[T any](value T) (T, error) {
	var result T
	data, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}
func object(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(data, &result)
	return result, err
}
func (r Record) inputs() (map[string]any, error) {
	values := map[string]any{}
	for name, value := range r.Collected.Fields {
		values[name] = value
	}
	observations := map[string]any{}
	for name, value := range r.Collected.Observations {
		observations[name] = value.Output
	}
	values["observations"] = observations
	pin, err := object(r.TargetRepo)
	if err != nil {
		return nil, err
	}
	values["targetRepo"] = pin
	if r.RecipeSource != nil {
		pinned, err := object(r.RecipeSource)
		if err != nil {
			return nil, err
		}
		values["recipeSource"] = pinned
	}
	return values, nil
}
func observationInput(ctx context.Context, spec Observation, inputs map[string]any) (Observation, string, error) {
	fields, err := object(spec)
	if err != nil {
		return Observation{}, "", err
	}
	resolved, err := Evaluate(ctx, fields, inputs)
	if err != nil {
		return Observation{}, "", err
	}
	data, err := json.Marshal(resolved)
	if err != nil {
		return Observation{}, "", err
	}
	var result Observation
	if err = json.Unmarshal(data, &result); err != nil {
		return result, "", err
	}
	hash := sha256.Sum256(data)
	return result, hex.EncodeToString(hash[:]), nil
}

// Normalize invalidates stale observations in dependency order. A changed host
// invalidates its observations even when the new machine has the same disk name.
// A partially collected record is valid but cannot render.
func (r *Record) Normalize(ctx context.Context) error {
	if err := r.TargetRepo.Validate(); err != nil {
		return err
	}
	if r.RecipeSource != nil {
		if err := r.RecipeSource.Pin.Validate(); err != nil {
			return err
		}
	}
	order, err := r.SourceDefinition.order()
	if err != nil {
		return err
	}
	if r.Collected.Fields == nil {
		r.Collected.Fields = map[string]any{}
	}
	if r.Collected.Observations == nil {
		r.Collected.Observations = map[string]CollectedObservation{}
	}
	for name := range r.Collected.Fields {
		if _, ok := r.SourceDefinition.Fields[name]; !ok {
			delete(r.Collected.Fields, name)
		}
	}
	for name := range r.Collected.Observations {
		if _, ok := r.SourceDefinition.Observations[name]; !ok {
			delete(r.Collected.Observations, name)
		}
	}
	for _, node := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		inputs, err := r.inputs()
		if err != nil {
			return err
		}
		if node.observation {
			observed, exists := r.Collected.Observations[node.name]
			if !exists {
				continue
			}
			_, hash, err := observationInput(ctx, r.SourceDefinition.Observations[node.name], inputs)
			if !r.ready(node) || err != nil || hash != observed.InputHash {
				delete(r.Collected.Observations, node.name)
			}
		} else {
			value, exists := r.Collected.Fields[node.name]
			if !exists {
				continue
			}
			if !r.ready(node) || !r.SourceDefinition.Fields[node.name].accepts(ctx, value, inputs) {
				delete(r.Collected.Fields, node.name)
			}
		}
	}
	r.CanRender = len(r.Collected.Fields) == len(r.SourceDefinition.Fields) && len(r.Collected.Observations) == len(r.SourceDefinition.Observations)
	return nil
}

func (f Field) accepts(ctx context.Context, value any, inputs map[string]any) bool {
	switch f.Type {
	case "text":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "select", "multiselect":
		choices, err := Evaluate(ctx, f.Values, inputs)
		if err != nil {
			return false
		}
		options, ok := choices.([]any)
		if !ok {
			return false
		}
		valid := func(candidate any) bool {
			for _, option := range options {
				resolved := option
				if f.OptionValue != "" {
					resolved, err = Evaluate(ctx, f.OptionValue, option)
					if err != nil {
						return false
					}
				}
				if reflect.DeepEqual(candidate, resolved) {
					return true
				}
			}
			return false
		}
		if f.Type == "select" {
			return valid(value)
		}
		values, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range values {
			if !valid(item) {
				return false
			}
		}
		return true
	}
	return false
}

// Collect computes a new snapshot. Persistence assigns a new version; this
// function never mutates the caller's record or edits an existing version.
func Collect(ctx context.Context, record Record, name string, collector func(context.Context, Observation) (CollectedObservation, error)) (Record, error) {
	result, err := clone(record)
	if err != nil {
		return result, err
	}
	if err = result.Normalize(ctx); err != nil {
		return result, err
	}
	if name != "" {
		if _, ok := result.SourceDefinition.Observations[name]; !ok {
			return result, errors.New("unknown observation")
		}
	}
	order, err := result.SourceDefinition.order()
	if err != nil {
		return result, err
	}
	for _, node := range order {
		if !node.observation || (name != "" && node.name != name) {
			continue
		}
		if !result.ready(node) {
			if name != "" {
				return result, errors.New("observation dependencies are not collected")
			}
			continue
		}
		inputs, err := result.inputs()
		if err != nil {
			return result, err
		}
		spec, hash, err := observationInput(ctx, result.SourceDefinition.Observations[node.name], inputs)
		if err != nil {
			if name != "" {
				return result, err
			}
			continue
		}
		observed, err := collector(ctx, spec)
		if err != nil {
			return result, fmt.Errorf("observe %s: %w", node.name, err)
		}
		observed.InputHash = hash
		observed.CollectedAt = time.Now().UTC()
		result.Collected.Observations[node.name] = observed
		if err = result.Normalize(ctx); err != nil {
			return result, err
		}
	}
	return result, nil
}

func keys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
