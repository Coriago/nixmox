package cluster

import (
	"context"
	"errors"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/natsx"
)

func (s *Supervisor) ForgetHost(ctx context.Context, id, route string, known ...string) error {
	s.mu.RLock()
	emb := s.emb
	s.mu.RUnlock()
	if emb != nil {
		kv, err := emb.JS.KeyValue(ctx, "hosts")
		if err == nil {
			if err = kv.Purge(ctx, "hosts."+id); err != nil {
				return err
			}
		} else if !errors.Is(err, jetstream.ErrBucketNotFound) {
			return err
		}
	}
	return s.ForgetRoute(ctx, route, known...)
}

// PrepareLeave reduces replicated metadata before shrinking below quorum.
func (s *Supervisor) PrepareLeave(ctx context.Context, remaining int) error {
	if remaining >= 3 {
		return nil
	}
	s.mu.RLock()
	emb := s.emb
	clustered := s.inClusterMode
	s.mu.RUnlock()
	if emb == nil || !clustered {
		return nil
	}
	for _, bucket := range []string{"hosts", "applylog"} {
		if _, err := emb.JS.KeyValue(ctx, bucket); errors.Is(err, jetstream.ErrBucketNotFound) {
			continue
		} else if err != nil {
			return err
		}
		if err := natsx.ScaleReplicas(ctx, emb.JS, bucket, 1); err != nil {
			return err
		}
	}
	return nil
}
