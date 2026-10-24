package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	js "github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/domain"
)

func recordKey(kind string, id domain.ID) string {
	return fmt.Sprintf("%s.%x", kind, sha256.Sum256([]byte(id)))
}

func marshalResult(value any, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func readRecord[T any](ctx context.Context, store js.KeyValue, key string) (T, uint64, error) {
	var value T
	entry, err := store.Get(ctx, key)
	if err != nil {
		return value, 0, err
	}
	err = json.Unmarshal(entry.Value(), &value)
	return value, entry.Revision(), err
}

// Consume the library's initial snapshot, not subsequent live updates.
func listRecords[T any](ctx context.Context, store js.KeyValue, pattern string) ([]T, error) {
	watch, err := store.Watch(ctx, pattern, js.IgnoreDeletes())
	if err != nil {
		return nil, err
	}
	defer watch.Stop()
	items := []T{}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case entry, ok := <-watch.Updates():
			if !ok {
				return nil, errors.New("state watch closed before snapshot completed")
			}
			if entry == nil {
				return items, nil
			}
			if len(items) == 5000 {
				return nil, errors.New("state snapshot exceeds bound")
			}
			var value T
			if err := json.Unmarshal(entry.Value(), &value); err != nil {
				return nil, err
			}
			items = append(items, value)
		}
	}
}

func writeRecord(ctx context.Context, store js.KeyValue, key string, revision uint64, value any) (uint64, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	if revision == 0 {
		return store.Create(ctx, key, data)
	}
	return store.Update(ctx, key, data, revision)
}
