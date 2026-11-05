// controller.go defines the common entry point for event- or poll-triggered reconciliation.
package main

import (
	"context"
	"errors"
)

// ErrNotImplemented makes unfinished operations explicit rather than reporting success.
var ErrNotImplemented = errors.New("example implementation is not written yet")

// Controller reconciles current desired and observed state.
// Watchers and periodic retries invoke the same method.
type Controller interface {
	Reconcile(context.Context) error
}
