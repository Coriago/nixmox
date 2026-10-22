package cluster

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/broker"
	"nixmox/internal/domain"
	"nixmox/internal/service"
)

type telemetryEntry struct {
	sample   domain.HostTelemetry
	received time.Time
}
type telemetryTransport struct {
	mu         sync.Mutex
	cache      map[string]telemetryEntry
	nc         *nats.Conn
	sub        *nats.Subscription
	cancel     context.CancelFunc
	done       chan struct{}
	manager    *Manager
	service    *service.Service
	membership Membership
}

func startTelemetry(m *Manager, b *broker.Broker, s *service.Service, membership Membership) (*telemetryTransport, error) {
	// Activity delivery must never queue behind durable command/result traffic,
	// replay reconnect buffers, or require JetStream availability.
	nc, err := nats.Connect(b.URL(), nats.Secure(b.ClientTLS()), nats.TLSHandshakeFirst(), nats.Timeout(2*time.Second), nats.MaxReconnects(-1), nats.ReconnectWait(time.Second), nats.ReconnectBufSize(-1), nats.NoEcho(), nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &telemetryTransport{cache: map[string]telemetryEntry{}, nc: nc, cancel: cancel, done: make(chan struct{}), manager: m, service: s, membership: membership}
	if membership.Provisional {
		t.sub, err = nc.Subscribe("nx."+membership.ClusterID+".activity.*", t.receive)
		if err == nil {
			err = t.sub.SetPendingLimits(128, 4<<20)
		}
		if err != nil {
			cancel()
			nc.Close()
			return nil, err
		}
	}
	ch, unsubscribe, err := s.SubscribeTelemetry()
	if err != nil {
		cancel()
		nc.Close()
		return nil, err
	}
	go func() {
		defer close(t.done)
		defer unsubscribe()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var stream string
		var sequence uint64
		publish := func() {
			value := s.LocalTelemetry()
			if value.ObservedAt == nil || value.MembershipEpoch == nil || *value.MembershipEpoch != membership.Epoch || value.Freshness != "live" || !nc.IsConnected() {
				return
			}
			if value.StreamID == stream && value.Sequence == sequence {
				return
			}
			data, err := json.Marshal(value)
			if err != nil || len(data) > domain.MaxCommandBytes {
				return
			}
			if nc.Publish("nx."+membership.ClusterID+".activity."+membership.NodeID, data) == nil {
				stream = value.StreamID
				sequence = value.Sequence
			}
		}
		publish()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				publish()
			case <-ticker.C:
				publish()
				t.expire(time.Now())
			}
		}
	}()
	return t, nil
}
func (t *telemetryTransport) Close() { t.cancel(); t.nc.Close(); <-t.done }
func (t *telemetryTransport) receive(msg *nats.Msg) {
	var value domain.HostTelemetry
	if len(msg.Data) > domain.MaxCommandBytes || domain.StrictJSON(msg.Data, &value) != nil || value.SchemaVersion != 1 || value.MembershipEpoch == nil || value.ObservedAt == nil || value.Sequence == 0 || !domain.SafeID.MatchString(value.StreamID) || msg.Subject != "nx."+t.membership.ClusterID+".activity."+value.HostID || value.HostID == t.membership.NodeID || value.IntervalSeconds < 0.25 || value.IntervalSeconds > 5 {
		return
	}
	now := time.Now()
	if value.ObservedAt.After(now.Add(5*time.Second)) || value.At(now).Freshness != "live" {
		return
	}
	member, err := t.manager.member(value.HostID)
	if err != nil || member.Epoch != *value.MembershipEpoch {
		return
	}
	t.mu.Lock()
	old, exists := t.cache[value.HostID]
	if exists && old.sample.MembershipEpoch != nil && *old.sample.MembershipEpoch == *value.MembershipEpoch {
		if old.sample.StreamID == value.StreamID && old.sample.Sequence >= value.Sequence {
			t.mu.Unlock()
			return
		}
		// Observations from a retired stream cannot displace the authenticated
		// node's newer restart stream, including delayed in-flight packets.
		if old.sample.ObservedAt != nil && value.ObservedAt.Before(*old.sample.ObservedAt) {
			t.mu.Unlock()
			return
		}
	}
	if !exists && len(t.cache) >= 256 {
		t.mu.Unlock()
		return
	}
	t.cache[value.HostID] = telemetryEntry{sample: value, received: now}
	t.mu.Unlock()
	t.service.NotifyTelemetry()
}
func (t *telemetryTransport) expire(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, entry := range t.cache {
		if now.Sub(entry.received) > 5*time.Minute {
			delete(t.cache, id)
		}
	}
}
func (m *Manager) RemoteTelemetry(node string) (domain.HostTelemetry, error) {
	member, err := m.member(node)
	if err != nil {
		return domain.HostTelemetry{}, err
	}
	m.mu.RLock()
	transport := m.telemetry
	m.mu.RUnlock()
	value := domain.UnknownTelemetry(node)
	epoch := member.Epoch
	value.MembershipEpoch = &epoch
	if transport == nil {
		return value, nil
	}
	transport.mu.Lock()
	entry, ok := transport.cache[node]
	transport.mu.Unlock()
	if ok && entry.sample.MembershipEpoch != nil && *entry.sample.MembershipEpoch == member.Epoch && time.Since(entry.received) <= 5*time.Minute {
		return entry.sample.At(time.Now()), nil
	}
	return value, nil
}
