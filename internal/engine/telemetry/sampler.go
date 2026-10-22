package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

var _ engine.HostSampler = (*Sampler)(nil)

type Sampler struct {
	mu          sync.Mutex
	sample      domain.HostTelemetry
	fast, slow  time.Duration
	subscribers map[*shared.Latest[domain.HostTelemetry]]bool
	running     bool
}

func NewSampler(node domain.ID, epoch *uint64, fast, slow time.Duration) (*Sampler, error) {
	if node == "" || fast < 250*time.Millisecond || fast > 5*time.Second || slow < time.Second || slow > time.Minute || slow < fast {
		return nil, errors.New("invalid sampling intervals")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &Sampler{fast: fast, slow: slow, subscribers: map[*shared.Latest[domain.HostTelemetry]]bool{}, sample: domain.HostTelemetry{SchemaVersion: 1, HostID: node, MembershipEpoch: epoch, StreamID: domain.ID(hex.EncodeToString(id[:])), IntervalSeconds: fast.Seconds(), Observed: domain.Observed{Freshness: domain.Unknown}, Filesystems: domain.Filesystems{Observed: domain.Observed{Freshness: domain.Unknown}, IntervalSeconds: slow.Seconds()}}}, nil
}

func sampleFreshness(t *time.Time, interval time.Duration, now time.Time) domain.Freshness {
	if t == nil {
		return domain.Unknown
	}
	if now.Sub(*t) > max(3*time.Second, 3*interval) {
		return domain.Stale
	}
	return domain.Live
}

func (s *Sampler) Snapshot(context.Context) domain.HostTelemetry {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := shared.Clone(s.sample)
	now := time.Now()
	snapshot.Freshness = sampleFreshness(snapshot.ObservedAt, s.fast, now)
	snapshot.Filesystems.Freshness = sampleFreshness(snapshot.Filesystems.ObservedAt, s.slow, now)
	return snapshot
}

type sampleSubscription struct {
	latest *shared.Latest[domain.HostTelemetry]
	owner  *Sampler
	once   sync.Once
}

func (s *sampleSubscription) Next(ctx context.Context) (domain.HostTelemetry, error) {
	return s.latest.Next(ctx)
}

func (s *sampleSubscription) Close() error {
	s.once.Do(func() { s.owner.mu.Lock(); delete(s.owner.subscribers, s.latest); s.owner.mu.Unlock() })
	return nil
}

func (s *Sampler) Subscribe(ctx context.Context) (engine.Subscription[domain.HostTelemetry], error) {
	latest := shared.NewLatest[domain.HostTelemetry]()
	s.mu.Lock()
	s.subscribers[latest] = true
	latest.Publish(shared.Clone(s.sample))
	s.mu.Unlock()
	sub := &sampleSubscription{owner: s, latest: latest}
	context.AfterFunc(ctx, func() { sub.Close() })
	return sub, nil
}

func (s *Sampler) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("sampler already started")
	}
	s.running = true
	s.mu.Unlock()
	// A filesystem syscall may be uninterruptible. There is only one slow worker;
	// shutdown/fast sampling never waits for it and never spawns replacements.
	go s.filesystems(ctx)
	ticker := time.NewTicker(s.fast)
	defer ticker.Stop()
	var previous *cpu.TimesStat
	var priorTime time.Time
	oldDisks := map[string]disk.IOCountersStat{}
	oldNetwork := map[string]gnet.IOCountersStat{}
	for {
		now := time.Now()
		sample := s.Snapshot(ctx)
		sample.Sequence++
		sample.Observed = domain.Observed{ObservedAt: shared.Ptr(now.UTC()), Freshness: domain.Live}
		sample.Unavailable = nil
		elapsed := now.Sub(priorTime).Seconds()
		failed := func(field string, err error) {
			if err != nil {
				sample.Unavailable = append(sample.Unavailable, domain.MeasurementError{Field: field, Reason: "collection failed"})
			}
		}
		sample.CPU = &domain.CPU{}
		times, err := cpu.TimesWithContext(ctx, false)
		failed("cpu", err)
		if err == nil && len(times) == 1 {
			current := times[0]
			if previous != nil {
				total := cpuTotal(current) - cpuTotal(*previous)
				idle := current.Idle - previous.Idle + current.Iowait - previous.Iowait
				if total > 0 && idle >= 0 && idle <= total {
					sample.CPU.Utilization = shared.Ptr((total - idle) / total)
				}
			}
			previous = &current
		} else {
			previous = nil
		}
		count, err := cpu.CountsWithContext(ctx, true)
		failed("cpu.logical_count", err)
		if err == nil {
			sample.CPU.LogicalCount = &count
		}
		averages, err := load.AvgWithContext(ctx)
		failed("cpu.load_average", err)
		if err == nil {
			sample.CPU.LoadAverage = &[3]float64{averages.Load1, averages.Load5, averages.Load15}
		}
		sample.Memory = nil
		memory, err := mem.VirtualMemoryWithContext(ctx)
		failed("memory", err)
		if err == nil {
			sample.Memory = &domain.Memory{TotalBytes: shared.Ptr(memory.Total), AvailableBytes: shared.Ptr(memory.Available), UsedBytes: shared.Ptr(memory.Total - memory.Available)}
		}
		sample.Swap = nil
		swap, err := mem.SwapMemoryWithContext(ctx)
		failed("swap", err)
		if err == nil {
			sample.Swap = &domain.Swap{TotalBytes: shared.Ptr(swap.Total), UsedBytes: shared.Ptr(swap.Used)}
		}
		sample.UptimeSeconds = nil
		uptime, err := host.UptimeWithContext(ctx)
		failed("uptime_seconds", err)
		if err == nil {
			sample.UptimeSeconds = shared.Ptr(float64(uptime))
		}
		counters, err := disk.IOCountersWithContext(ctx)
		failed("disks", err)
		sample.Disks = nil
		if err == nil {
			for name, v := range counters {
				old, ok := oldDisks[name]
				sample.Disks = append(sample.Disks, domain.DiskIO{Device: name, ReadBytes: shared.Ptr(v.ReadBytes), WriteBytes: shared.Ptr(v.WriteBytes), ReadBytesPerSecond: shared.Rate(old.ReadBytes, v.ReadBytes, elapsed, ok), WriteBytesPerSecond: shared.Rate(old.WriteBytes, v.WriteBytes, elapsed, ok)})
			}
		}
		oldDisks = counters
		sort.Slice(sample.Disks, func(i, j int) bool { return sample.Disks[i].Device < sample.Disks[j].Device })
		network, err := gnet.IOCountersWithContext(ctx, true)
		failed("network", err)
		sample.Network = nil
		nextNetwork := map[string]gnet.IOCountersStat{}
		if err == nil {
			for _, v := range network {
				old, ok := oldNetwork[v.Name]
				nextNetwork[v.Name] = v
				sample.Network = append(sample.Network, domain.NetworkIO{Interface: v.Name, RXBytes: shared.Ptr(v.BytesRecv), TXBytes: shared.Ptr(v.BytesSent), RXBytesPerSecond: shared.Rate(old.BytesRecv, v.BytesRecv, elapsed, ok), TXBytesPerSecond: shared.Rate(old.BytesSent, v.BytesSent, elapsed, ok)})
			}
		}
		oldNetwork = nextNetwork
		priorTime = now
		s.mu.Lock()
		sample.Filesystems = s.sample.Filesystems
		s.sample = sample
		for sub := range s.subscribers {
			sub.Publish(shared.Clone(sample))
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func cpuTotal(c cpu.TimesStat) float64 {
	return c.User + c.System + c.Nice + c.Idle + c.Iowait + c.Irq + c.Softirq + c.Steal
}

func (s *Sampler) filesystems(ctx context.Context) {
	ticker := time.NewTicker(s.slow)
	defer ticker.Stop()
	for {
		result := domain.Filesystems{IntervalSeconds: s.slow.Seconds(), Items: []domain.Filesystem{}}
		partitions, err := disk.PartitionsWithContext(ctx, false)
		seen := map[string]bool{}
		if err == nil {
			for _, p := range partitions {
				if ctx.Err() != nil {
					return
				}
				if seen[p.Device] || !strings.HasPrefix(p.Device, "/dev/") {
					continue
				}
				seen[p.Device] = true
				if len(result.Items) >= 128 {
					break
				}
				usage, e := disk.UsageWithContext(ctx, p.Mountpoint)
				if e != nil {
					err = e
					continue
				}
				result.Items = append(result.Items, domain.Filesystem{Mount: p.Mountpoint, TotalBytes: shared.Ptr(usage.Total), UsedBytes: shared.Ptr(usage.Used), AvailableBytes: shared.Ptr(usage.Free)})
			}
		}
		if err == nil {
			result.Observed = domain.Observed{ObservedAt: shared.Ptr(time.Now().UTC()), Freshness: domain.Live}
			s.mu.Lock()
			s.sample.Filesystems = result
			s.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SampleTracker accepts only a stream established by authenticated membership or
// restart observation. A sample alone cannot renew membership or switch streams.
type SampleTracker struct {
	mu       sync.Mutex
	epoch    uint64
	stream   domain.ID
	sample   *domain.HostTelemetry
	received time.Time
}

func (t *SampleTracker) Reset(epoch uint64, stream domain.ID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.epoch, t.stream = epoch, stream
	t.sample = nil
	t.received = time.Time{}
}

func (t *SampleTracker) Accept(sample domain.HostTelemetry) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if sample.MembershipEpoch == nil || *sample.MembershipEpoch != t.epoch || sample.StreamID != t.stream || (t.sample != nil && sample.Sequence <= t.sample.Sequence) {
		return false
	}
	copy := shared.Clone(sample)
	t.sample = &copy
	t.received = time.Now()
	return true
}

func (t *SampleTracker) Current(now time.Time) *domain.HostTelemetry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sample == nil || now.Sub(t.received) > domain.TelemetryRetention {
		return nil
	}
	copy := shared.Clone(*t.sample)
	copy.Freshness = sampleFreshness(copy.ObservedAt, time.Duration(copy.IntervalSeconds*float64(time.Second)), now)
	copy.Filesystems.Freshness = sampleFreshness(copy.Filesystems.ObservedAt, time.Duration(copy.Filesystems.IntervalSeconds*float64(time.Second)), now)
	return &copy
}
