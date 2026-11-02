package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
)

type Registry struct {
	Sources        Manager
	Repositories   cluster.Store[Repository]
	Configurations cluster.Store[Configuration]
	Run            func(context.Context, string, io.Reader, string, ...string) ([]byte, error)
}

func (r Registry) names(ctx context.Context, repo Repository, pin Pin, flakeDir string) ([]string, error) {
	if r.Run == nil {
		return nil, errors.New("Nix evaluator unavailable")
	}
	work, err := r.Sources.Checkout(ctx, repo, pin)
	if err != nil {
		return nil, err
	}
	defer work.Close()
	directory, err := work.Directory(flakeDir)
	if err != nil {
		return nil, err
	}
	output, err := r.Run(ctx, work.Path, nil, "nix", "eval", "--json", "--no-write-lock-file", "--no-update-lock-file", "path:"+directory+"#configurations.nixos", "--apply", "builtins.attrNames")
	if err != nil {
		return nil, fmt.Errorf("enumerate NixOS configurations: %w", err)
	}
	var names []string
	if err = json.Unmarshal(output, &names); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// Register discovers configurations from the pinned repository. Existing pins
// are preserved: refreshing a repository must not silently retarget deployments.
func (r Registry) Register(ctx context.Context, repo Repository, flakeDir string) ([]Configuration, error) {
	if flakeDir == "" {
		flakeDir = "."
	}
	pin := Pin{ID: repo.ID, Ref: repo.Ref, SHA: repo.SHA}
	names, err := r.names(ctx, repo, pin, flakeDir)
	if err != nil {
		return nil, err
	}
	result := make([]Configuration, 0, len(names))
	for _, name := range names {
		id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(repo.ID+"\x00"+flakeDir+"\x00"+name)).String()
		configuration := Configuration{ID: id, Name: name, TargetRepo: pin, FlakeDir: flakeDir, FlakeConfiguration: name}
		if _, err = r.Configurations.Create(ctx, id, configuration); errors.Is(err, jetstream.ErrKeyExists) {
			record, readErr := r.Configurations.Get(ctx, id)
			if readErr != nil {
				return nil, readErr
			}
			configuration = record.Value
		} else if err != nil {
			return nil, err
		}
		result = append(result, configuration)
	}
	return result, nil
}

func (r Registry) Refresh(ctx context.Context, id string) (Configuration, error) {
	current, err := r.Configurations.Get(ctx, id)
	if err != nil {
		return Configuration{}, err
	}
	tracked, err := r.Repositories.Get(ctx, current.Value.TargetRepo.ID)
	if err != nil {
		return Configuration{}, err
	}
	repo := tracked.Value
	repo.Ref = current.Value.TargetRepo.Ref
	repo, err = r.Sources.Refresh(ctx, repo)
	if err != nil {
		return Configuration{}, err
	}
	pin := Pin{ID: repo.ID, Ref: repo.Ref, SHA: repo.SHA}
	names, err := r.names(ctx, repo, pin, current.Value.FlakeDir)
	if err != nil {
		return Configuration{}, err
	}
	found := false
	for _, name := range names {
		if name == current.Value.FlakeConfiguration {
			found = true
			break
		}
	}
	if !found {
		return Configuration{}, errors.New("configuration is absent at the new repository revision")
	}
	updated := current.Value
	updated.TargetRepo = pin
	_, err = r.Configurations.Update(ctx, id, updated, current.Revision)
	return updated, err
}
