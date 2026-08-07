package natsx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

type BucketConfig struct {
	TTL time.Duration
}

// EnsureKV idempotently returns a KV bucket, creating it at R=1 when missing.
// Buckets use the library default AllowDirect=true; direct-get staleness is
// moot at R=1 since a single replica has no followers.
func EnsureKV(ctx context.Context, js jetstream.JetStream, name string, cfg BucketConfig) (jetstream.KeyValue, error) {
	kv, err := js.KeyValue(ctx, name)
	if err == nil {
		return kv, nil
	}
	if !errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, fmt.Errorf("lookup kv %s: %w", name, err)
	}
	kv, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   name,
		Replicas: 1,
		TTL:      cfg.TTL,
		History:  1,
	})
	if err != nil {
		return nil, fmt.Errorf("create kv %s: %w", name, err)
	}
	return kv, nil
}

// ScaleReplicas updates the stream backing the named KV bucket. replicas must
// never be 2: with 2 copies, quorum is 2 — losing either peer makes the bucket
// read-only, strictly worse than R=1.
func ScaleReplicas(ctx context.Context, js jetstream.JetStream, name string, replicas int) error {
	if replicas == 2 {
		return fmt.Errorf("replicas=2 is forbidden (worse than R=1, no fault tolerance)")
	}
	streamName := "KV_" + name
	s, err := js.Stream(ctx, streamName)
	if err != nil {
		return fmt.Errorf("lookup stream %s: %w", streamName, err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		return fmt.Errorf("stream info %s: %w", streamName, err)
	}
	if info.Config.Replicas == replicas {
		return nil
	}
	cfg := info.Config
	cfg.Replicas = replicas
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("scale %s to R=%d: %w", streamName, replicas, err)
	}
	return nil
}
