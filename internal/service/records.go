package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/comms"
)

func recordKey(kind string, id domain.ID) string {
	return fmt.Sprintf("%s.%x", kind, sha256.Sum256([]byte(id)))
}
func readRecord[T any](ctx context.Context, store engine.StateStore, key string) (T, engine.Revision, error) {
	var value T
	entry, err := store.Get(ctx, key)
	if err == nil {
		err = json.Unmarshal(entry.Value, &value)
	}
	return value, entry.Revision, err
}

// listRecords uses the transport's bounded initial snapshot, never an open-ended watch.
func listRecords[T any](ctx context.Context, store *comms.JetStream, pattern string) ([]T, error) {
	entries, err := store.Entries(ctx, pattern)
	if err != nil {
		return nil, err
	}
	items := make([]T, 0, len(entries))
	for _, entry := range entries {
		var value T
		if err := json.Unmarshal(entry.Value, &value); err != nil {
			return nil, err
		}
		items = append(items, value)
	}
	return items, nil
}

func writeRecord(ctx context.Context, store engine.StateStore, key string, revision engine.Revision, value any) (engine.Revision, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	return store.CompareAndSwap(ctx, key, revision, data)
}
