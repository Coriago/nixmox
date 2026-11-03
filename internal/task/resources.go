// Package task owns durable task transitions and local execution handles.
package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/cluster"
)

var ErrBusy = errors.New("exclusive resource is in use")

// Resources atomically acquires the whole resource set with a single KV CAS.
// Locks have no TTL: losing connectivity must not authorize a competing runner.
// Recovery releases locks only after proving the previous owner stopped.
type Resources struct {
	Store cluster.Store[map[string]string]
}

func (r Resources) Acquire(ctx context.Context, taskID string, resources []string) error {
	return r.change(ctx, func(owners map[string]string) error {
		for _, resource := range resources {
			if owner := owners[resource]; owner != "" && owner != taskID {
				return ErrBusy
			}
		}
		for _, resource := range resources {
			owners[resource] = taskID
		}
		return nil
	})
}

func (r Resources) Release(ctx context.Context, taskID string) error {
	return r.change(ctx, func(owners map[string]string) error {
		for resource, owner := range owners {
			if owner == taskID {
				delete(owners, resource)
			}
		}
		return nil
	})
}

func (r Resources) change(ctx context.Context, change func(map[string]string) error) error {
	for attempt := 0; attempt < 32; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := r.Store.Get(ctx, "resources")
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			_, err = r.Store.Create(ctx, "resources", map[string]string{})
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
