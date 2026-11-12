package stores

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/examples/jetstream/records"
)

type MembershipSnapshot struct {
	Ready bool // false means initial sync incomplete, not an empty cluster.
	Hosts map[string]records.MemberRecord
}

type MembershipStore struct {
	JS    jetstream.JetStream
	KV    jetstream.KeyValue
	cache MembershipSnapshot
}

func (s *MembershipStore) Create(ctx context.Context) error {
	// Create MEMBERS with initial replication; existing config is not overwritten.
	return errors.ErrUnsupported
}

func (s *MembershipStore) Open(ctx context.Context) error {
	// Obtain existing MEMBERS handle; fail/retry if unavailable, never create it.
	return errors.ErrUnsupported
}

func (s *MembershipStore) UpdateReplicas(ctx context.Context, count int) error {
	// Explicitly update ONLY this bucket's backing stream and verify readiness.
	return errors.ErrUnsupported
}

func (s *MembershipStore) Watch(ctx context.Context, observer Observer) error {
	// This store owns one independent KV WatchAll and handler.
	// Decode each section, set its Revision from entry.Revision(), then update cache.
	// Only after updating cache call StoreChanged(Membership).
	// Track initial sync, partial hosts, deletions and stale revisions.
	// Snapshot access/publication must be safe; concurrency mechanism is not chosen yet.
	// Recover watch state if interrupted; never run controller work inside this handler.
	return errors.ErrUnsupported
}

func (s *MembershipStore) Snapshot() MembershipSnapshot {
	// Return an immutable/copy view; caller must not see ongoing cache mutation.
	return MembershipSnapshot{}
}
