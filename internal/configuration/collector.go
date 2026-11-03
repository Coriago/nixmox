package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/host"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
)

type Collector struct {
	Members       cluster.Store[domain.Member]
	Repositories  cluster.Store[source.Repository]
	CredentialIDs func(context.Context) ([]string, error)
	HostDisks     func(context.Context, string) (any, error)
	HostHardware  func(context.Context, string, bool) (string, error)
	HostAgeKey    func(context.Context, string) (string, error)
	Sources       source.Manager
	Process       host.Process
}

// Each built-in has one implementation; lifecycle and persistence stay in Service.
var observationCollectors = map[recipe.ObservationType]func(Collector, context.Context, recipe.Observation) (ObservationResult, error){
	recipe.ClusterHosts:    Collector.collectClusterHosts,
	recipe.ClusterRepos:    Collector.collectClusterRepos,
	recipe.ClusterCreds:    Collector.collectClusterCredentials,
	recipe.HostDisks:       Collector.collectHostDisks,
	recipe.HostHardware:    Collector.collectHostHardware,
	recipe.HostSSHtoAgeKey: Collector.collectHostAgeKey,
	recipe.RepoFile:        Collector.collectRepositoryFile,
	recipe.RepoCommand:     Collector.collectRepositoryCommand,
}

// Collect dispatches a resolved observation. Host callbacks must target the
// requested host; unavailable remote operations never fall back to local ones.
func (c Collector) Collect(ctx context.Context, observation recipe.Observation) (ObservationResult, error) {
	collect, supported := observationCollectors[observation.Type]
	if !supported {
		return ObservationResult{}, recipe.Invalid(fmt.Errorf("unsupported observation type %q", observation.Type))
	}
	result, err := collect(c, ctx, observation)
	if err != nil {
		return result, err
	}
	return recipe.Canonical(result)
}

func (c Collector) collectClusterHosts(ctx context.Context, observation recipe.Observation) (ObservationResult, error) {
	result := ObservationResult{Host: observation.Host, Path: observation.Path}
	if c.Members.KV == nil {
		return result, errors.New("membership store unavailable")
	}
	members, err := listAll(ctx, c.Members)
	if err != nil {
		return result, err
	}
	hosts := make([]map[string]string, 0, len(members))
	for _, member := range members {
		if member.Status == "removed" {
			continue
		}
		hosts = append(hosts, map[string]string{"id": member.NodeID, "name": member.Name})
	}
	result.Output = hosts
	return result, nil
}

func (c Collector) collectClusterRepos(ctx context.Context, observation recipe.Observation) (ObservationResult, error) {
	result := ObservationResult{Host: observation.Host, Path: observation.Path}
	if c.Repositories.KV == nil {
		return result, errors.New("repository store unavailable")
	}
	repositories, err := listAll(ctx, c.Repositories)
	result.Output = repositories
	return result, err
}

func (c Collector) collectClusterCredentials(ctx context.Context, observation recipe.Observation) (ObservationResult, error) {
	result := ObservationResult{Host: observation.Host, Path: observation.Path}
	if c.CredentialIDs == nil {
		return result, errors.New("credential metadata unavailable")
	}
	ids, err := c.CredentialIDs(ctx)
	result.Output = ids
	return result, err
}

func (c Collector) collectHostDisks(ctx context.Context, observation recipe.Observation) (ObservationResult, error) {
	result := ObservationResult{Host: observation.Host, Path: observation.Path}
	if observation.Host == "" {
		return result, recipe.Invalid(errors.New("observation host is required"))
	}
	if c.HostDisks == nil {
		return result, errors.New("host disk service unavailable")
	}
	output, err := c.HostDisks(ctx, observation.Host)
	result.Output = output
	return result, err
}

func (c Collector) collectHostHardware(ctx context.Context, observation recipe.Observation) (ObservationResult, error) {
	result := ObservationResult{Host: observation.Host, Path: observation.Path}
	if observation.Host == "" {
		return result, recipe.Invalid(errors.New("observation host is required"))
	}
	if c.HostHardware == nil {
		return result, errors.New("host hardware service unavailable")
	}
	output, err := c.HostHardware(ctx, observation.Host, observation.NoFileSystems)
	result.Output = output
	return result, err
}

func (c Collector) collectHostAgeKey(ctx context.Context, observation recipe.Observation) (ObservationResult, error) {
	result := ObservationResult{Host: observation.Host, Path: observation.Path}
	if observation.Host == "" {
		return result, recipe.Invalid(errors.New("observation host is required"))
	}
	if c.HostAgeKey == nil {
		return result, errors.New("host public-key service unavailable")
	}
	output, err := c.HostAgeKey(ctx, observation.Host)
	result.Output = output
	return result, err
}

func (c Collector) collectRepositoryFile(ctx context.Context, observation recipe.Observation) (ObservationResult, error) {
	result := ObservationResult{Host: observation.Host, Path: observation.Path}
	work, pin, err := c.checkoutObservation(ctx, observation.Repo)
	if err != nil {
		return result, err
	}
	defer work.Close()
	result.Repo = &pin

	data, err := work.ReadFile(observation.Path)
	if err != nil {
		return result, err
	}
	if len(data) > recipe.RenderLimit {
		return result, errors.New("observation output exceeds 1 MiB")
	}
	result.Output = string(data)
	return result, nil
}

func (c Collector) collectRepositoryCommand(ctx context.Context, observation recipe.Observation) (ObservationResult, error) {
	result := ObservationResult{Host: observation.Host, Path: observation.Path}
	if strings.TrimSpace(observation.Cmd) == "" {
		return result, recipe.Invalid(errors.New("repository command is required"))
	}
	work, pin, err := c.checkoutObservation(ctx, observation.Repo)
	if err != nil {
		return result, err
	}
	defer work.Close()
	result.Repo = &pin

	// Commands are operator-authorized programs, not arguments interpolated into
	// daemon-generated shell commands. Execute only in the pinned worktree.
	data, err := c.Process.Run(ctx, work.Path, nil, "sh", "-c", observation.Cmd)
	if err != nil {
		return result, err
	}
	if len(data) > recipe.RenderLimit {
		return result, errors.New("observation output exceeds 1 MiB")
	}
	if json.Unmarshal(data, &result.Output) != nil {
		result.Output = string(data)
	}
	return result, nil
}

func (c Collector) checkoutObservation(ctx context.Context, reference any) (*source.Worktree, source.Pin, error) {
	repository, pin, err := c.repository(ctx, reference)
	if err != nil {
		return nil, pin, err
	}
	work, err := c.Sources.Checkout(ctx, repository, pin)
	return work, pin, err
}

func listAll[T any](ctx context.Context, store cluster.Store[T]) ([]T, error) {
	result := []T{}
	cursor := ""
	for {
		records, next, err := store.List(ctx, cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			result = append(result, record.Value)
		}
		if next == "" {
			return result, nil
		}
		cursor = next
	}
}

func (c Collector) repository(ctx context.Context, value any) (source.Repository, source.Pin, error) {
	var requested struct {
		RootDir string `json:"rootDir"`
		ID      string `json:"id"`
		URL     string `json:"url"`
		Ref     string `json:"ref"`
		SHA     string `json:"sha"`
	}
	var repo source.Repository
	var pin source.Pin
	data, err := json.Marshal(value)
	if err != nil {
		return repo, pin, err
	}
	if err = json.Unmarshal(data, &requested); err != nil {
		return repo, pin, errors.New("observation repo must resolve to a repository reference")
	}
	if c.Repositories.KV == nil {
		return repo, pin, errors.New("repository store unavailable")
	}
	if requested.ID != "" {
		record, err := c.Repositories.Get(ctx, requested.ID)
		if err != nil {
			return repo, pin, err
		}
		repo = record.Value
	} else {
		if requested.URL == "" || requested.Ref == "" {
			return repo, pin, errors.New("observation repository URL and reference required")
		}
		tracked, err := listAll(ctx, c.Repositories)
		if err != nil {
			return repo, pin, err
		}
		for _, candidate := range tracked {
			if candidate.URL == requested.URL && candidate.Ref == requested.Ref {
				repo = candidate
				break
			}
		}
		if repo.ID == "" {
			id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(requested.URL+"\x00"+requested.Ref)).String()
			repo, err = c.Sources.Refresh(ctx, source.Repository{ID: id, Name: requested.URL, URL: requested.URL, Ref: requested.Ref})
			if err != nil {
				return repo, pin, err
			}
			if _, err = c.Repositories.Create(ctx, id, repo); err != nil && !errors.Is(err, jetstream.ErrKeyExists) {
				return repo, pin, err
			}
		}
	}
	if requested.Ref == "" {
		requested.Ref = repo.Ref
	}
	if requested.SHA == "" {
		resolve := repo
		resolve.Ref = requested.Ref
		refreshed, err := c.Sources.Refresh(ctx, resolve)
		if err != nil {
			return repo, pin, err
		}
		requested.SHA = refreshed.SHA
	}
	if requested.RootDir == "" {
		requested.RootDir = repo.RootDir
	}
	pin = source.Pin{ID: repo.ID, Ref: requested.Ref, SHA: requested.SHA, RootDir: requested.RootDir}
	return repo, pin, pin.Validate()
}
