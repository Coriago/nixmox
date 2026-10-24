package service

import (
	"context"
	"encoding/json"
	"errors"

	"nixmox/internal/domain"
	"nixmox/internal/engine/comms"
)

// One event handler owns configuration reactions. It never wakes on a timer.
func (s *Service) events(ctx context.Context, transport *comms.Runtime) error {
	c := s.Cluster
	transport.Core.OnConnectionChange(c.signal)
	defer transport.Core.OnConnectionChange(func() {})
	stop, err := transport.Core.ObserveMembership(c.signal)
	if err != nil {
		return err
	}
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.events:
			_ = c.pollMembership(ctx)
			_ = c.reconcileTopology(ctx)
			s.syncCredentials(ctx)
			_ = s.Autojoin(ctx)
		}
	}
}

// KV's initial snapshot also handles startup. Membership notifications contain no
// trust material; recipients fetch and verify signed notices/topology themselves.
func (s *Service) watchMembership(ctx context.Context, transport *comms.Runtime) error {
	watch, err := transport.Store.KV.Watch(ctx, "membership")
	if err != nil {
		return err
	}
	defer watch.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case entry, ok := <-watch.Updates():
			if !ok {
				return errors.New("membership watch stopped")
			}
			if entry == nil {
				continue
			}
			var state domain.MembershipState
			if err := json.Unmarshal(entry.Value(), &state); err != nil {
				return err
			}
			for id := range state.Members {
				_ = transport.Core.NotifyMembership(ctx, id)
				if !state.Members[id].Removed {
					if err := transport.Store.PrepareTasks(ctx, id); err != nil {
						return err
					}
				}
			}
			s.Cluster.signal()
		}
	}
}
