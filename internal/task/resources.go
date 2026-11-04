// Package task owns durable task transitions and local execution handles.
package task

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
)

var ErrBusy = errors.New("exclusive resource is in use")

// Resources uses one native KV CAS per ownership scope. Host scopes permit NATS
// to restrict pending writers to their own locks. A runner starts only after the
// complete sorted set is acquired; partial acquisition is rolled back. Locks
// have no TTL and are never stolen because a client disconnected.
type Resources struct {
	Store cluster.Store[map[string]string]
}

func resourceScope(resource string) (string, error) {
	parts := strings.Split(resource, ":")
	if len(parts) != 3 || (parts[0] != "host" && parts[0] != "cluster") || parts[1] == "" || parts[2] == "" || strings.ContainsAny(parts[1], ".*> /\\\t\r\n") {
		return "", errors.New("resource must be host:ID:NAME or cluster:ID:NAME")
	}
	return parts[0] + "." + parts[1], nil
}

func (r Resources) Acquire(ctx context.Context, taskID string, resources []string) error {
	if len(resources) == 0 {
		return nil
	}
	groups := map[string][]string{}
	for _, resource := range resources {
		scope, err := resourceScope(resource)
		if err != nil {
			return err
		}
		groups[scope] = append(groups[scope], resource)
	}
	// Honor unfinished tasks created before scoped lock storage was introduced.
	legacy, err := r.Store.Get(ctx, "resources")
	if err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) && !errors.Is(err, jetstream.ErrKeyDeleted) {
		return err
	}
	for _, resource := range resources {
		if owner := legacy.Value[resource]; owner != "" && owner != taskID {
			return ErrBusy
		}
	}
	scopes := make([]string, 0, len(groups))
	for scope := range groups {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	acquired := map[string][]string{}
	for _, scope := range scopes {
		var added []string
		err = r.change(ctx, scope, func(owners map[string]string) error {
			added = nil
			for _, resource := range groups[scope] {
				if owner := owners[resource]; owner != "" && owner != taskID {
					return ErrBusy
				}
			}
			for _, resource := range groups[scope] {
				if owners[resource] == "" {
					added = append(added, resource)
				}
				owners[resource] = taskID
			}
			return nil
		})
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			for acquiredScope, names := range acquired {
				releaseErr := r.change(cleanup, acquiredScope, func(owners map[string]string) error {
					for _, name := range names {
						if owners[name] == taskID {
							delete(owners, name)
						}
					}
					return nil
				})
				err = errors.Join(err, releaseErr)
			}
			return err
		}
		acquired[scope] = added
	}
	return nil
}

func (r Resources) Release(ctx context.Context, taskID string) error {
	keys, err := r.Store.KV.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, key := range keys {
		record, err := r.Store.Get(ctx, key)
		if err != nil {
			return err
		}
		owned := false
		for _, owner := range record.Value {
			if owner == taskID {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		if err = r.change(ctx, key, func(owners map[string]string) error {
			for resource, owner := range owners {
				if owner == taskID {
					delete(owners, resource)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r Resources) change(ctx context.Context, key string, change func(map[string]string) error) error {
	for attempt := 0; attempt < 32; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := r.Store.Get(ctx, key)
		if errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, jetstream.ErrKeyDeleted) {
			_, err = r.Store.Create(ctx, key, map[string]string{})
			if err == nil || errors.Is(err, jetstream.ErrKeyExists) {
				continue
			}
		}
		if err != nil {
			return err
		}
		if record.Value == nil {
			return errors.New("invalid resource ownership record")
		}
		if err = change(record.Value); err != nil {
			return err
		}
		_, err = r.Store.Update(ctx, record.Key, record.Value, record.Revision)
		if err == nil {
			return nil
		}
		if !errors.Is(err, jetstream.ErrKeyExists) {
			return err
		}
	}
	return fmt.Errorf("resource ownership contention: %w", ErrBusy)
}
