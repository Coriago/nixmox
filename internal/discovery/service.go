package discovery

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/cluster"
)

// Store selects live shared storage after joining. The boolean distinguishes an
// unjoined node from a disconnected member; members never fall back to local data.
type Service struct {
	Observer string
	Store    func() (cluster.Store[Record], bool)
	mu       sync.Mutex
	local    map[string]Record
}

func (s *Service) storage() (cluster.Store[Record], bool) {
	if s.Store != nil {
		store, joined := s.Store()
		if joined {
			s.mu.Lock()
			s.local = nil
			s.mu.Unlock()
		}
		return store, joined
	}
	return cluster.Store[Record]{}, false
}

func (s *Service) Observe(ctx context.Context, advertisement Advertisement) error {
	if err := advertisement.Validate(); err != nil {
		return err
	}
	if advertisement.NodeID == s.Observer {
		return nil
	}
	record := Record{ID: recordID(s.Observer, advertisement), ObservedByHostID: s.Observer, LastSeenAt: time.Now().UTC(), Advertisement: advertisement}
	store, joined := s.storage()
	if joined {
		if store.KV == nil {
			return errors.New("discovery store unavailable")
		}
		previous, err := store.Get(ctx, record.ID)
		if errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, jetstream.ErrKeyDeleted) {
			_, err = store.Create(ctx, record.ID, record)
		} else if err == nil {
			// Bound storage writes even if a peer floods duplicate advertisements.
			if time.Since(previous.Value.LastSeenAt) < time.Second {
				return nil
			}
			_, err = store.Update(ctx, record.ID, record, previous.Revision)
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	if s.local == nil {
		s.local = map[string]Record{}
	}
	if _, exists := s.local[record.ID]; !exists && len(s.local) >= 512 {
		return errors.New("discovery capacity reached")
	}
	s.local[record.ID] = record
	return nil
}

func (s *Service) prune() {
	for id, record := range s.local {
		if time.Since(record.LastSeenAt) >= Lifetime {
			delete(s.local, id)
		}
	}
}

func (s *Service) List(ctx context.Context, after string, limit int) ([]Record, string, error) {
	if limit < 1 || limit > 500 {
		return nil, "", errors.New("invalid discovery limit")
	}
	store, joined := s.storage()
	if joined {
		if store.KV == nil {
			return nil, "", errors.New("discovery store unavailable")
		}
		values, next, err := store.List(ctx, after, limit)
		result := []Record{}
		for _, value := range values {
			result = append(result, value.Value)
		}
		return result, next, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	keys := []string{}
	for key := range s.local {
		if key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	next := ""
	if len(keys) > limit {
		keys = keys[:limit]
		next = keys[len(keys)-1]
	}
	result := []Record{}
	for _, key := range keys {
		result = append(result, s.local[key])
	}
	return result, next, nil
}

func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	store, joined := s.storage()
	if joined {
		if store.KV == nil {
			return Record{}, errors.New("discovery store unavailable")
		}
		value, err := store.Get(ctx, id)
		return value.Value, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	value, ok := s.local[id]
	if !ok {
		return Record{}, jetstream.ErrKeyNotFound
	}
	return value, nil
}
