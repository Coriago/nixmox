// Package engine defines shared contracts. Concrete implementations live in the
// comms, telemetry, task, nix and git subpackages; feature workflows live in service.
package engine

import (
	"context"
	"io"

	"nixmox/internal/domain"
)

type ObjectStore interface {
	// Immutable recipe payloads, never repository snapshots, Nix outputs or secrets.
	Put(context.Context, domain.StoredObject, io.Reader) error
	Open(context.Context, domain.StoredObject) (io.ReadCloser, error)
	Delete(context.Context, domain.StoredObject) error
}
type Subscription[T any] interface {
	Next(context.Context) (T, error)
	Close() error
}
