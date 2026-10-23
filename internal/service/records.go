package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
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
func writeRecord(ctx context.Context, store engine.StateStore, key string, revision engine.Revision, value any) (engine.Revision, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	return store.CompareAndSwap(ctx, key, revision, data)
}
