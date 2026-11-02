package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/host"
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

// Collect implements the built-in observation types. Host functions are routed
// by the daemon to the owning host; this layer never executes a remote host's
// operation locally merely because that host cannot be reached.
func (c Collector) Collect(ctx context.Context, spec Observation) (CollectedObservation, error) {
	result := CollectedObservation{Host: spec.Host, Path: spec.Path}
	var err error
	switch spec.Type {
	case "clusterHosts":
		if c.Members.KV == nil {
			return result, errors.New("membership store unavailable")
		}
		members, err := all(ctx, c.Members)
		if err != nil {
			return result, err
		}
		hosts := make([]map[string]string, 0, len(members))
		for _, member := range members {
			if member.Status != "removed" {
				hosts = append(hosts, map[string]string{"id": member.NodeID, "name": member.Name})
			}
		}
		result.Output = hosts
	case "clusterRepos":
		if c.Repositories.KV == nil {
			return result, errors.New("repository store unavailable")
		}
		result.Output, err = all(ctx, c.Repositories)
	case "clusterCreds":
		if c.CredentialIDs == nil {
			return result, errors.New("credential metadata unavailable")
		}
		result.Output, err = c.CredentialIDs(ctx)
	case "hostDisks", "hostHardware", "hostSSHtoAgeKey":
		if spec.Host == "" {
			return result, errors.New("observation host is required")
		}
		switch spec.Type {
		case "hostDisks":
			if c.HostDisks == nil {
				return result, errors.New("host disk service unavailable")
			}
			result.Output, err = c.HostDisks(ctx, spec.Host)
		case "hostHardware":
			if c.HostHardware == nil {
				return result, errors.New("host hardware service unavailable")
			}
			result.Output, err = c.HostHardware(ctx, spec.Host, spec.NoFileSystems)
		case "hostSSHtoAgeKey":
			if c.HostAgeKey == nil {
				return result, errors.New("host public-key service unavailable")
			}
			result.Output, err = c.HostAgeKey(ctx, spec.Host)
		}
	case "repoFile", "repoCommand":
		repo, pin, resolveErr := c.repository(ctx, spec.Repo)
		if resolveErr != nil {
			return result, resolveErr
		}
		work, checkoutErr := c.Sources.Checkout(ctx, repo, pin)
		if checkoutErr != nil {
			return result, checkoutErr
		}
		defer work.Close()
		result.Repo = &pin
		var data []byte
		if spec.Type == "repoFile" {
			data, err = work.ReadFile(spec.Path)
		} else {
			if strings.TrimSpace(spec.Cmd) == "" {
				return result, errors.New("repository command is required")
			}
			// Recipe commands are operator-authorized programs, not untrusted user
			// arguments interpolated into a daemon-generated shell command.
			data, err = c.Process.Run(ctx, work.Path, nil, "sh", "-c", spec.Cmd)
		}
		if err != nil {
			return result, err
		}
		if len(data) > RenderLimit {
			return result, errors.New("observation output exceeds 1 MiB")
		}
		if spec.Type == "repoFile" || json.Unmarshal(data, &result.Output) != nil {
			result.Output = string(data)
		}
	default:
		return result, errors.New("unsupported observation type")
	}
	if err != nil {
		return result, err
	}
	// Normalize JSON-shaped collections for typed field selection and templates.
	normalized, err := clone(result)
	return normalized, err
}

func all[T any](ctx context.Context, store cluster.Store[T]) ([]T, error) {
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
		ID  string `json:"id"`
		URL string `json:"url"`
		Ref string `json:"ref"`
		SHA string `json:"sha"`
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
		tracked, err := all(ctx, c.Repositories)
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
	pin = source.Pin{ID: repo.ID, Ref: requested.Ref, SHA: requested.SHA}
	return repo, pin, pin.Validate()
}
