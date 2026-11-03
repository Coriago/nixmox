// Package cluster provides NATS-backed state without local record mirrors.
package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"nixmox/internal/util"
)

// Record keeps storage revisions out of the domain payload. Writes always use
// Create or compare-and-swap; there is deliberately no unconditional Put.
type Record[T any] struct {
	Key      string `json:"key"`
	Revision uint64 `json:"revision"`
	Value    T      `json:"value"`
}

type Store[T any] struct{ KV jetstream.KeyValue }

func (s Store[T]) Get(ctx context.Context, key string) (Record[T], error) {
	ctx, span := otel.Tracer("nixmox.cluster").Start(ctx, "records.get")
	defer span.End()
	entry, err := s.KV.Get(ctx, key)
	if err != nil {
		span.RecordError(util.Error(err))
		span.SetStatus(codes.Error, "record read failed")
		return Record[T]{}, err
	}
	return decode[T](entry)
}

func decode[T any](entry jetstream.KeyValueEntry) (Record[T], error) {
	record := Record[T]{Key: entry.Key(), Revision: entry.Revision()}
	err := json.Unmarshal(entry.Value(), &record.Value)
	return record, err
}

func (s Store[T]) Create(ctx context.Context, key string, value T) (uint64, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	return s.KV.Create(ctx, key, data)
}

func (s Store[T]) Update(ctx context.Context, key string, value T, revision uint64) (uint64, error) {
	if revision == 0 {
		return 0, errors.New("update requires a revision")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	return s.KV.Update(ctx, key, data, revision)
}

func (s Store[T]) Delete(ctx context.Context, key string, revision uint64) error {
	if revision == 0 {
		return errors.New("delete requires a revision")
	}
	return s.KV.Delete(ctx, key, jetstream.LastRevision(revision))
}

// List reads directly from JetStream. The cursor is the last key, not an offset;
// concurrent inserts may appear on later pages, not a fictitious frozen snapshot.
func (s Store[T]) List(ctx context.Context, after string, limit int) ([]Record[T], string, error) {
	if limit < 1 || limit > 500 {
		return nil, "", errors.New("limit must be between 1 and 500")
	}
	keys, err := s.KV.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return []Record[T]{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	sort.Strings(keys)
	records := make([]Record[T], 0, limit)
	for _, key := range keys {
		if key <= after {
			continue
		}
		record, err := s.Get(ctx, key)
		if errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, jetstream.ErrKeyDeleted) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if len(records) == limit {
			return records, records[len(records)-1].Key, nil
		}
		records = append(records, record)
	}
	return records, "", nil
}

// Watch delivers the initial state followed by updates through one native
// JetStream watcher, avoiding a list/subscribe gap. nil marks initial completion.
func (s Store[T]) Watch(ctx context.Context, pattern string, handle func(*Record[T], bool) error) error {
	watcher, err := s.KV.Watch(ctx, pattern)
	if err != nil {
		return err
	}
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case entry, ok := <-watcher.Updates():
			if !ok {
				return errors.New("record watcher closed")
			}
			if entry == nil {
				if err := handle(nil, false); err != nil {
					return err
				}
				continue
			}
			deleted := entry.Operation() != jetstream.KeyValuePut
			record := Record[T]{Key: entry.Key(), Revision: entry.Revision()}
			if !deleted {
				record, err = decode[T](entry)
				if err != nil {
					return fmt.Errorf("decode %s: %w", entry.Key(), err)
				}
			}
			if err := handle(&record, deleted); err != nil {
				return err
			}
		}
	}
}

// OpenStore does not create or change server configuration. Only cluster
// coordinators provision buckets, using the current quorum plan.
func OpenStore[T any](ctx context.Context, js jetstream.JetStream, bucket string) (Store[T], error) {
	for {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		kv, err := js.KeyValue(probe, bucket)
		cancel()
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			return Store[T]{KV: kv}, err
		}
		// Metadata can be ready before this bucket's restored Raft group.
		// Retry only the read, never an uncertain mutation.
		select {
		case <-ctx.Done():
			return Store[T]{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
