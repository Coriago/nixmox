package recipe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"

	"nixmox/internal/cluster"
	"nixmox/internal/source"
)

// Service owns definition lifecycle; it never collects configuration values.
type Service struct {
	Repository
	Sources      source.Manager
	Repositories cluster.Store[source.Repository]
}

type Edit struct {
	Name       *string     `json:"name,omitempty"`
	Definition *Definition `json:"sourceDefinition,omitempty"`
}

func (s Service) Create(ctx context.Context, name string, definition Definition) (Version, error) {
	return s.save(ctx, Version{
		ID:               uuid.NewString(),
		Version:          1,
		Name:             name,
		SourceDefinition: definition,
	})
}

func (s Service) Import(ctx context.Context, name string, origin Source) (Version, error) {
	definition, err := s.read(ctx, origin)
	if err != nil {
		return Version{}, err
	}
	return s.save(ctx, Version{
		ID:               uuid.NewString(),
		Version:          1,
		Name:             name,
		RecipeSource:     &origin,
		SourceDefinition: definition,
	})
}

func (s Service) Edit(ctx context.Context, id string, expected uint64, edit Edit) (Version, error) {
	current, err := s.current(ctx, id, expected)
	if err != nil {
		return Version{}, err
	}
	next, err := Canonical(current)
	if err != nil {
		return Version{}, err
	}
	if edit.Name != nil {
		next.Name = *edit.Name
	}
	if edit.Definition != nil {
		next.SourceDefinition, err = Canonical(*edit.Definition)
		if err != nil {
			return Version{}, err
		}
	}
	if reflect.DeepEqual(next, current) {
		return current, nil
	}
	next.Version++
	if current.RecipeSource != nil {
		// Local edits fork tracked recipes but retain their pinned template inputs.
		next.ID = uuid.NewString()
		next.Version = 1
		origin := current.Ref()
		next.DerivedFrom = &origin
		if next.Dependencies == nil {
			next.Dependencies = map[string]source.Pin{}
		}
		next.Dependencies["recipeSource"] = current.RecipeSource.Pin
		next.RecipeSource = nil
	}
	return s.save(ctx, next)
}

func (s Service) Sync(ctx context.Context, id string, expected uint64) (Version, error) {
	current, err := s.current(ctx, id, expected)
	if err != nil {
		return Version{}, err
	}
	if current.RecipeSource == nil {
		return Version{}, errors.New("recipe does not track a repository")
	}
	repo, err := s.Repositories.Get(ctx, current.RecipeSource.ID)
	if err != nil {
		return Version{}, err
	}
	tracked := repo.Value
	tracked.Ref = current.RecipeSource.Ref
	refreshed, err := s.Sources.Refresh(ctx, tracked)
	if err != nil {
		return Version{}, err
	}
	origin := *current.RecipeSource
	origin.SHA = refreshed.SHA
	definition, err := s.read(ctx, origin)
	if err != nil {
		return Version{}, err
	}
	if reflect.DeepEqual(definition, current.SourceDefinition) {
		return current, nil
	}
	current.Version++
	current.RecipeSource = &origin
	current.SourceDefinition = definition
	return s.save(ctx, current)
}

func (s Service) read(ctx context.Context, origin Source) (Definition, error) {
	if err := origin.Pin.Validate(); err != nil {
		return Definition{}, Invalid(err)
	}
	if !filepath.IsLocal(origin.Path) {
		return Definition{}, errors.New("recipe source path must be local")
	}
	repo, err := s.Repositories.Get(ctx, origin.ID)
	if err != nil {
		return Definition{}, err
	}
	work, err := s.Sources.Checkout(ctx, repo.Value, origin.Pin)
	if err != nil {
		return Definition{}, err
	}
	defer work.Close()
	data, err := work.ReadFile(origin.Path)
	if err != nil {
		return Definition{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var definition Definition
	if err = decoder.Decode(&definition); err != nil {
		return Definition{}, err
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Definition{}, errors.New("recipe source must contain one YAML document")
	}
	return Canonical(definition)
}
