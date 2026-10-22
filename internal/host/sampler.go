package host

import (
	"context"
	"nixmox/internal/domain"
	"sync"
	"time"
)

type Sampler struct {
	mu      sync.RWMutex
	sample  domain.HostSample
	cancel  context.CancelFunc
	done    chan struct{}
	refresh chan struct{}
}

func StartSampler(inspector Inspector, identity func() domain.Identity, publish func(domain.HostSample) error) *Sampler {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Sampler{cancel: cancel, done: make(chan struct{}), refresh: make(chan struct{}, 1)}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			round, stop := context.WithTimeout(ctx, 10*time.Second)
			sample, err := inspector.Inspect(round, identity().NodeID)
			stop()
			if err == nil {
				s.mu.Lock()
				s.sample = sample
				s.mu.Unlock()
				_ = publish(sample)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.refresh:
			}
		}
	}()
	return s
}
func (s *Sampler) Refresh() {
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}
func (s *Sampler) Snapshot() domain.HostSample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sample := s.sample
	sample.Endpoints = append([]domain.Endpoint(nil), sample.Endpoints...)
	sample.Health.Reasons = append([]domain.HealthReason(nil), sample.Health.Reasons...)
	return sample
}
func (s *Sampler) Close() { s.cancel(); <-s.done }
