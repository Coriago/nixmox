// Package configuration realizes pinned recipe versions into deployable configurations.
package configuration

import (
	"time"

	"nixmox/internal/recipe"
	"nixmox/internal/source"
)

type ObservationResult struct {
	CollectedAt time.Time   `json:"collectedAt"`
	Repo        *source.Pin `json:"repo,omitempty"`
	Host        string      `json:"host,omitempty"`
	Path        string      `json:"path,omitempty"`
	Output      any         `json:"output"`
	InputHash   string      `json:"inputHash"`
}

type Collected struct {
	Fields       map[string]any               `json:"fields"`
	Observations map[string]ObservationResult `json:"observations"`
}

type Version struct {
	Schema             int               `json:"schema"`
	ID                 string            `json:"id"`
	Version            uint64            `json:"version"`
	Name               string            `json:"name"`
	Recipe             *recipe.Reference `json:"recipe,omitempty"`
	TargetRepo         source.Pin        `json:"targetRepo"`
	FlakeDir           string            `json:"flakeDir,omitempty"`
	FlakeConfiguration string            `json:"flakeConfiguration,omitempty"`
	CanRender          bool              `json:"canRender"`
	Collected          Collected         `json:"collected"`
}

type Create struct {
	Name               string            `json:"name"`
	Recipe             *recipe.Reference `json:"recipe,omitempty"`
	TargetRepo         source.Pin        `json:"targetRepo"`
	Fields             map[string]any    `json:"fields,omitempty"`
	FlakeDir           string            `json:"flakeDir,omitempty"`
	FlakeConfiguration string            `json:"flakeConfiguration,omitempty"`
}
