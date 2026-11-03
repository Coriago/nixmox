package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/cluster"
	"nixmox/internal/source"
)

type Registry struct {
	Sources        source.Manager
	Repositories   cluster.Store[source.Repository]
	Configurations *Service
	Run            func(context.Context, string, io.Reader, string, ...string) ([]byte, error)
}

func (r Registry) names(ctx context.Context, repo source.Repository, pin source.Pin, flakeDir string) ([]string, error) {
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
	output, err := r.Run(ctx, work.Path, nil, "nix",
		"eval", "--json",
		"--no-write-lock-file", "--no-update-lock-file",
		"path:"+directory+"#configurations.nixos",
		"--apply", "builtins.attrNames",
	)
	if err != nil {
		return nil, fmt.Errorf("enumerate NixOS configurations: %w", err)
	}
	var names []string
	if err = json.Unmarshal(output, &names); err != nil {
		return nil, err
	}
	slices.Sort(names)
	return names, nil
}

// Register discovers configurations from the pinned repository. Existing pins
// are preserved: refreshing a repository must not silently retarget deployments.
func (r Registry) Register(ctx context.Context, repo source.Repository, flakeDir string) ([]Version, error) {
	if flakeDir == "" {
		flakeDir = "."
	}
	pin := repo.Pin()
	names, err := r.names(ctx, repo, pin, flakeDir)
	if err != nil {
		return nil, err
	}
	result := make([]Version, 0, len(names))
	for _, name := range names {
		id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(repo.ID+"\x00"+flakeDir+"\x00"+name)).String()
		configuration, err := r.Configurations.save(ctx, Version{
			ID:                 id,
			Version:            1,
			Name:               name,
			TargetRepo:         pin,
			FlakeDir:           flakeDir,
			FlakeConfiguration: name,
		})
		if errors.Is(err, jetstream.ErrKeyExists) {
			configuration, err = r.Configurations.Latest(ctx, id)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, configuration)
	}
	return result, nil
}

func (r Registry) Refresh(ctx context.Context, id string) (Version, error) {
	current, err := r.Configurations.Latest(ctx, id)
	if err != nil {
		return Version{}, err
	}
	if current.Recipe != nil {
		return Version{}, errors.New("recipe-backed configurations require explicit target-pin refresh")
	}
	tracked, err := r.Repositories.Get(ctx, current.TargetRepo.ID)
	if err != nil {
		return Version{}, err
	}
	repo := tracked.Value
	repo.Ref = current.TargetRepo.Ref
	repo, err = r.Sources.Refresh(ctx, repo)
	if err != nil {
		return Version{}, err
	}
	pin := current.TargetRepo
	pin.SHA = repo.SHA
	names, err := r.names(ctx, repo, pin, current.FlakeDir)
	if err != nil {
		return Version{}, err
	}
	if !slices.Contains(names, current.FlakeConfiguration) {
		return Version{}, errors.New("configuration is absent at the new repository revision")
	}
	return r.Configurations.RefreshTarget(ctx, id, current.Version, pin)
}
