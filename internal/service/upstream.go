package service

import (
	"context"
	"errors"
	"time"

	js "github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/domain"
)

type upstreamObservation struct {
	RepositoryID domain.ID     `json:"repository_id"`
	Commit       domain.Commit `json:"commit"`
	ObservedAt   time.Time     `json:"observed_at"`
}

// One poller observes upstream refs. It never fetches objects, changes accepted
// repository pins, or schedules deployments implicitly.
func (s *Service) PollGit(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = s.pollGit(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (s *Service) pollGit(ctx context.Context) error {
	c := s.Cluster
	c.mu.Lock()
	store, authority := c.transport.Store, c.creator()
	c.mu.Unlock()
	if !authority || store == nil {
		return nil
	}
	repositories, err := listRecords[domain.RepositoryState](ctx, store.KV, "repository.*")
	if err != nil {
		return err
	}
	for _, state := range repositories {
		call, cancel := context.WithTimeout(ctx, domain.ExportTimeout)
		repository := state.Repository
		holder := s.Identity.NodeID
		c.mu.Lock()
		if repository.Remote.CredentialRef != nil {
			record, _, lookup := c.credentialLocked(call, *repository.Remote.CredentialRef)
			if lookup == nil {
				holder, lookup = c.credentialHolderLocked(record)
			}
			err = lookup
		} else {
			err = nil
		}
		c.mu.Unlock()
		var head domain.Commit
		if err == nil {
			if holder == s.Identity.NodeID {
				head, err = c.git.RemoteHead(call, repository)
			} else {
				err = c.callResult(call, holder, "source.upstream", repository, false, &head)
			}
		}
		if err == nil {
			key := recordKey("upstream", repository.ID)
			old, revision, readErr := readRecord[upstreamObservation](call, store.KV, key)
			if readErr == nil || errors.Is(readErr, js.ErrKeyNotFound) {
				observation := upstreamObservation{repository.ID, head, time.Now().UTC()}
				if _, writeErr := writeRecord(call, store.KV, key, revision, observation); writeErr == nil && old.Commit != head {
					c.mu.Lock()
					core := c.transport.Core
					c.mu.Unlock()
					if core != nil {
						_ = core.PublishUpstream(call, repository.ID, head)
					}
				}
			}
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}
