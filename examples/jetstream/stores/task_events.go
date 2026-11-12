package stores

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/examples/jetstream/records"
)

// Task commands are a durable stream, NOT a bucket cache/dispatcher wake-up.
type TaskEvents struct {
	JS       jetstream.JetStream
	Stream   jetstream.Stream
	Consumer jetstream.Consumer
}

func (s *TaskEvents) Create(ctx context.Context) error {
	// Provision TASK_EVENTS at cluster creation.
	return errors.ErrUnsupported
}

func (s *TaskEvents) Open(ctx context.Context) error {
	// Open the existing stream when joining/reconnecting.
	return errors.ErrUnsupported
}

func (s *TaskEvents) UpdateReplicas(ctx context.Context, count int) error {
	// Update this stream explicitly, independently of KV bucket replication.
	return errors.ErrUnsupported
}

func (s *TaskEvents) Publish(ctx context.Context, event records.TaskEvent) error {
	// Publish to taskevents.<host>.<action>, using EventID for delivery deduplication.
	return errors.ErrUnsupported
}

func (s *TaskEvents) Consume(ctx context.Context, hostID string, handle func(context.Context, records.TaskEvent) error) error {
	// Use a host-filtered durable NATS consumer to invoke the task scheduler.
	// Ack only after Handle persists desired state; retry transient failures.
	// This is a command path, never dispatched/coalesced as a store notification.
	return errors.ErrUnsupported
}
