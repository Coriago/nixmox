package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/cluster"
	"nixmox/internal/recipe"
)

// Repository persists immutable configuration versions using create-only KV writes.
type Repository struct{ Records cluster.Store[Version] }

func (r Repository) Get(ctx context.Context, id string, version uint64) (Version, error) {
	key, err := cluster.VersionKey(id, version)
	if err != nil {
		return Version{}, err
	}
	record, err := r.Records.Get(ctx, key)
	if err != nil {
		return Version{}, err
	}
	if record.Value.Schema != 2 {
		return Version{}, errors.New("legacy configuration schema requires explicit migration")
	}
	return record.Value, nil
}

func (r Repository) Latest(ctx context.Context, id string) (Version, error) {
	versions, err := cluster.VersionNumbers(ctx, r.Records.KV)
	if err != nil {
		return Version{}, err
	}
	if versions[id] == 0 {
		return Version{}, jetstream.ErrKeyNotFound
	}
	return r.Get(ctx, id, versions[id])
}

func (r Repository) current(ctx context.Context, id string, expected uint64) (Version, error) {
	current, err := r.Latest(ctx, id)
	if err != nil {
		return Version{}, err
	}
	if expected == 0 || expected != current.Version {
		return Version{}, fmt.Errorf("configuration version changed: %w", jetstream.ErrKeyExists)
	}
	if expected == ^uint64(0) {
		return Version{}, errors.New("configuration version exhausted")
	}
	return current, nil
}

func (r Repository) save(ctx context.Context, value Version) (Version, error) {
	value, err := recipe.Canonical(value)
	if err != nil {
		return Version{}, err
	}
	value.Schema = 2
	data, err := json.Marshal(value)
	if err != nil {
		return Version{}, err
	}
	if len(data) > 900<<10 {
		return Version{}, recipe.Invalid(errors.New("version exceeds shared-record size limit"))
	}
	key, err := cluster.VersionKey(value.ID, value.Version)
	if err != nil {
		return Version{}, err
	}
	_, err = r.Records.Create(ctx, key, value)
	return value, err
}

func (r Repository) List(ctx context.Context, after string, limit int) ([]Version, string, error) {
	if limit < 1 || limit > 500 {
		return nil, "", errors.New("invalid page size")
	}
	versions, err := cluster.VersionNumbers(ctx, r.Records.KV)
	if err != nil {
		return nil, "", err
	}
	result := []Version{}
	for _, id := range slices.Sorted(maps.Keys(versions)) {
		if id <= after {
			continue
		}
		if len(result) == limit {
			return result, result[len(result)-1].ID, nil
		}
		value, err := r.Get(ctx, id, versions[id])
		if err != nil {
			return nil, "", err
		}
		result = append(result, value)
	}
	return result, "", nil
}
