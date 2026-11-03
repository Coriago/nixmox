package recipe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/source"
)

// Discover registers recipes once. Existing definitions advance only via Sync.
func (s Service) Discover(ctx context.Context, repo source.Repository) error {
	work, err := s.Sources.Checkout(ctx, repo, repo.Pin())
	if err != nil {
		return err
	}
	defer work.Close()
	root, err := os.OpenRoot(work.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Open(".nixmox/recipes")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		extension := filepath.Ext(entry.Name())
		if extension != ".yaml" && extension != ".yml" && extension != ".json" {
			continue
		}
		path := filepath.Join(".nixmox/recipes", entry.Name())
		id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(repo.ID+"\x00"+repo.RootDir+"\x00"+path)).String()
		if _, err = s.Latest(ctx, id); err == nil {
			continue
		} else if !errors.Is(err, jetstream.ErrKeyNotFound) {
			return err
		}
		origin := Source{Pin: repo.Pin(), Path: path}
		definition, err := s.read(ctx, origin)
		if err != nil {
			return err
		}
		_, err = s.save(ctx, Version{ID: id, Version: 1, Name: strings.TrimSuffix(entry.Name(), extension), RecipeSource: &origin, SourceDefinition: definition})
		if err != nil && !errors.Is(err, jetstream.ErrKeyExists) {
			return err
		}
	}
	return nil
}
