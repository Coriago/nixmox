// Package recipe owns reusable, immutable definitions and their source lifecycle.
package recipe

import (
	"encoding/json"

	"nixmox/internal/source"
)

type Reference struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

type Source struct {
	source.Pin
	Path string `json:"path"`
}

type Field struct {
	Type        string `json:"type" yaml:"type"`
	Values      any    `json:"values,omitempty" yaml:"values,omitempty"`
	OptionValue string `json:"optionValue,omitempty" yaml:"optionValue,omitempty"`
	OptionLabel string `json:"optionLabel,omitempty" yaml:"optionLabel,omitempty"`
}

// ObservationType identifies a built-in collector without changing its wire name.
type ObservationType string

const (
	ClusterHosts    ObservationType = "clusterHosts"
	ClusterRepos    ObservationType = "clusterRepos"
	ClusterCreds    ObservationType = "clusterCreds"
	HostDisks       ObservationType = "hostDisks"
	HostHardware    ObservationType = "hostHardware"
	HostSSHtoAgeKey ObservationType = "hostSSHtoAgeKey"
	RepoFile        ObservationType = "repoFile"
	RepoCommand     ObservationType = "repoCommand"
)

type Observation struct {
	Type          ObservationType `json:"type" yaml:"type"`
	Repo          any             `json:"repo,omitempty" yaml:"repo,omitempty"`
	Host          string          `json:"host,omitempty" yaml:"host,omitempty"`
	Cmd           string          `json:"cmd,omitempty" yaml:"cmd,omitempty"`
	Path          string          `json:"path,omitempty" yaml:"path,omitempty"`
	NoFileSystems bool            `json:"noFileSystems,omitempty" yaml:"noFileSystems,omitempty"`
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

type Version struct {
	Schema       int        `json:"schema"`
	ID           string     `json:"id"`
	Version      uint64     `json:"version"`
	Name         string     `json:"name"`
	RecipeSource *Source    `json:"recipeSource,omitempty"`
	DerivedFrom  *Reference `json:"derivedFrom,omitempty"`
	// Dependencies are pinned template inputs, never tracked locations. A fork
	// retains recipeSource here so source expressions need no textual rewriting.
	Dependencies     map[string]source.Pin `json:"dependencies,omitempty"`
	SourceDefinition Definition            `json:"sourceDefinition"`
}

func (v Version) Ref() Reference { return Reference{v.ID, v.Version} }

func (v Version) SourceInputs() map[string]any {
	inputs := map[string]any{}
	for name, pin := range v.Dependencies {
		inputs[name] = pin
	}
	if v.RecipeSource != nil {
		inputs["recipeSource"] = v.RecipeSource.Pin
	}
	return inputs
}

// Canonical uses the same JSON shape before and after persistence, including
// numbers nested in field choices. This avoids YAML-vs-JSON equality drift.
func Canonical[T any](value T) (T, error) {
	var result T
	data, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}

func Object(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(data, &result)
	return result, err
}
