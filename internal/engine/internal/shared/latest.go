package shared

import (
	"context"
	"sync/atomic"
)

// Latest is the shared one-slot coalescing primitive for sampler/gateway/UI updates.
// Closing belongs to the consumer context, avoiding send-on-closed-channel races.
type Latest[T any] struct {
	values  chan T
	Dropped atomic.Uint64
}

func NewLatest[T any]() *Latest[T] { return &Latest[T]{values: make(chan T, 1)} }

func (l *Latest[T]) Publish(v T) {
	select {
	case l.values <- v:
		return
	default:
	}
	select {
	case <-l.values:
		l.Dropped.Add(1)
	default:
	}
	select {
	case l.values <- v:
	default:
		l.Dropped.Add(1)
	}
}

func (l *Latest[T]) Next(ctx context.Context) (T, error) {
	select {
	case v := <-l.values:
		return v, nil
	case <-ctx.Done():
		var v T
		return v, ctx.Err()
	}
}
