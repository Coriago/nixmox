package telemetry

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"nixmox/internal/domain"
)

type Sampler struct {
	mu               sync.Mutex
	latest           domain.HostTelemetry
	filesystems      domain.FilesystemSample
	filesystemFailed bool
	subscribers      map[chan struct{}]struct{}
	cancel           context.CancelFunc
	done             chan struct{}
}

func Start(identity func() domain.Identity, fast, filesystem time.Duration) *Sampler {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Sampler{latest: domain.UnknownTelemetry(identity().NodeID), filesystems: domain.FilesystemSample{Freshness: "unknown", IntervalSeconds: filesystem.Seconds()}, subscribers: map[chan struct{}]struct{}{}, cancel: cancel, done: make(chan struct{})}
	// A single isolated worker bounds outstanding statfs calls. An unresponsive
	// mount cannot stop fast sampling, request handlers, or daemon shutdown.
	go func() {
		ticker := time.NewTicker(filesystem)
		defer ticker.Stop()
		for ctx.Err() == nil {
			value := filesystemSample("/proc", filesystem)
			s.mu.Lock()
			s.filesystemFailed = value.ObservedAt == nil
			if value.ObservedAt != nil {
				s.filesystems = value
			}
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(fast)
		defer ticker.Stop()
		reader := procReader{root: "/proc"}
		stream := domain.NewID("telemetry_")
		previousIdentity := identity()
		var sequence uint64
		for ctx.Err() == nil {
			id := identity()
			if id.MembershipEpoch != previousIdentity.MembershipEpoch || clusterID(id) != clusterID(previousIdentity) {
				reader.previous = baseline{}
				stream = domain.NewID("telemetry_")
				sequence = 0
				previousIdentity = id
			}
			value := reader.sample(time.Now())
			value.HostID = id.NodeID
			value.StreamID = stream
			sequence++
			value.Sequence = sequence
			value.IntervalSeconds = fast.Seconds()
			if id.ClusterID != nil {
				epoch := id.MembershipEpoch
				value.MembershipEpoch = &epoch
			}
			s.mu.Lock()
			value.Filesystems = s.filesystems
			if value.Filesystems.ObservedAt == nil || s.filesystemFailed {
				value.Unavailable = append(value.Unavailable, domain.MeasurementUnavailable{Field: "filesystems", Reason: "No successful filesystem observation is available."})
			}
			s.latest = value
			for ch := range s.subscribers {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return s
}
func clusterID(id domain.Identity) string {
	if id.ClusterID == nil {
		return ""
	}
	return *id.ClusterID
}
func (s *Sampler) Close() { s.cancel(); <-s.done }
func (s *Sampler) Snapshot() domain.HostTelemetry {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Callers never retain mutable buffers owned by the sampler.
	b, _ := json.Marshal(s.latest)
	var result domain.HostTelemetry
	_ = json.Unmarshal(b, &result)
	return result.At(time.Now())
}
func (s *Sampler) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() { s.mu.Lock(); delete(s.subscribers, ch); s.mu.Unlock() }
}
