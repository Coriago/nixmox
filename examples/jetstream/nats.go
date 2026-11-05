// nats.go defines the manager for embedded servers, clients, and native operations.
package main

import (
	"context"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NATSManager owns transports and servers, not membership policy or task progress.
type NATSManager struct {
	ID        string
	Directory string

	primary   *server.Server
	bootstrap *server.Server
	client    *nats.Conn
	system    *nats.Conn
	jetstream jetstream.JetStream
}

// StartPrimary will create or restore the primary without requiring shared KV first.
func (m *NATSManager) StartPrimary(ctx context.Context, routes []string) error {
	return ErrNotImplemented
}

// StartBootstrap will create or restore the additional local JetStream server.
func (m *NATSManager) StartBootstrap(ctx context.Context) error {
	return ErrNotImplemented
}

// StopPrimary will retain metadata membership, identity, and storage.
func (m *NATSManager) StopPrimary(ctx context.Context) error {
	return ErrNotImplemented
}

// StopBootstrap will retain an assigned bootstrap for a later restart.
func (m *NATSManager) StopBootstrap(ctx context.Context) error {
	return ErrNotImplemented
}

// Connect will open application and privileged system clients.
func (m *NATSManager) Connect(ctx context.Context, endpoints []string) error {
	return ErrNotImplemented
}

// Disconnect will close clients after local controllers stop.
func (m *NATSManager) Disconnect() error {
	return ErrNotImplemented
}

// Evacuate will request data movement; acceptance is not proof of completion.
func (m *NATSManager) Evacuate(ctx context.Context, serverName string) error {
	return ErrNotImplemented
}

// CheckHealth will verify native placement and quorum on the intended survivors.
func (m *NATSManager) CheckHealth(ctx context.Context, survivors []string, replicas int) error {
	return ErrNotImplemented
}

// SetReplicas will apply membership-based replication policy.
func (m *NATSManager) SetReplicas(ctx context.Context, replicas int) error {
	return ErrNotImplemented
}

// RetirePrimary will permanently remove an evacuated primary peer and its storage.
func (m *NATSManager) RetirePrimary(ctx context.Context) error {
	return ErrNotImplemented
}

// RetireBootstrap will permanently remove an obsolete bootstrap after safe handoff.
func (m *NATSManager) RetireBootstrap(ctx context.Context) error {
	return ErrNotImplemented
}
