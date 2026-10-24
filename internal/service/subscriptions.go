package service

import (
	"context"
	"encoding/json"
	"errors"

	"golang.org/x/sync/errgroup"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

// notifyTransport runs with c.mu held. Closing the channel wakes the single
// subscription owner; changes during shutdown coalesce into the next snapshot.
func (c *ClusterNode) notifyTransport() {
	if c.transportChanged != nil {
		close(c.transportChanged)
	}
	c.transportChanged = make(chan struct{})
}

// ServeNATS owns application subscriptions for the current authorized runtime.
// NATS handles reconnects. A replaced runtime cancels all handlers before the
// next generation starts; unexpected subscription errors stop the daemon.
func (s *Service) ServeNATS(ctx context.Context) error {
	c := s.Cluster
	for {
		c.mu.Lock()
		if c.transportChanged == nil {
			c.transportChanged = make(chan struct{})
		}
		changed, transport := c.transportChanged, c.transport
		store := transport.Store
		joined, authority := c.keys.Trust != nil, c.creator()
		secrets := joined && c.forced()
		var nodes []domain.ID
		if authority && c.state != nil && store != nil {
			for id, member := range c.state.Members {
				if !member.Removed {
					nodes = append(nodes, id)
				}
			}
		}
		c.mu.Unlock()
		if transport.Core == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
				continue
			}
		}
		life, cancel := context.WithCancel(transport.Context)
		stop := context.AfterFunc(ctx, cancel)
		group, life := errgroup.WithContext(life)
		group.Go(func() error { return s.events(life, transport) })
		if authority && store != nil {
			group.Go(func() error { return s.watchMembership(life, transport) })
		}
		kinds := []string{"query", "api"}
		if authority {
			kinds = append(kinds, "view")
		}
		for _, kind := range kinds {
			group.Go(func() error {
				var ready func()
				if kind == "query" {
					ready = c.signal
				}
				return transport.Core.ServeQueries(life, kind, func(ctx context.Context, q engine.Query) (json.RawMessage, error) {
					return s.query(ctx, transport.Core, q, kind == "view")
				}, ready)
			})
		}
		if secrets {
			group.Go(func() error { return transport.Core.ServeSecrets(life, s.Import) })
		}
		group.Go(func() error {
			if store != nil {
				if err := store.PrepareTasks(life, s.Identity.NodeID); err != nil {
					return err
				}
				for _, node := range nodes {
					if err := store.PrepareTasks(life, node); err != nil {
						return err
					}
				}
			} else {
				var ready domain.Empty
				if err := c.callResult(life, "", "tasks.prepare", domain.Empty{}, true, &ready); err != nil {
					return err
				}
			}
			consumers, run := errgroup.WithContext(life)
			consumers.Go(func() error { return s.consumeTasks(run, transport.API, transport.Core.Cluster, false) })
			consumers.Go(func() error { return s.consumeTasks(run, transport.API, transport.Core.Cluster, true) })
			return consumers.Wait()
		})
		if authority || !joined {
			group.Go(func() error {
				stop, err := transport.Core.ObserveTasks(s.observeTask)
				if err != nil {
					return err
				}
				defer stop()
				<-life.Done()
				return life.Err()
			})
		}
		for _, current := range s.Tasks.List() {
			_ = transport.Core.PublishTask(life, current)
		}
		done := make(chan error, 1)
		go func() { done <- group.Wait() }()
		var err error
		select {
		case <-changed:
			cancel()
			err = <-done
		case <-ctx.Done():
			cancel()
			err = <-done
		case err = <-done:
			// A broker replacement can close its connection before the service
			// publishes the replacement. Wait for that event, not a retry timer.
			if transport.Context.Err() != nil && ctx.Err() == nil {
				select {
				case <-changed:
				case <-ctx.Done():
				}
			}
		}
		stop()
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !errors.Is(err, context.Canceled) && transport.Context.Err() == nil {
			return err
		}
	}
}
