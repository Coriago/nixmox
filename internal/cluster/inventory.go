package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
	"nixmox/internal/state"
	"sort"
	"strings"
	"time"
)

func (m *Manager) Members(ctx context.Context) ([]domain.HostMembership, error) {
	m.mu.RLock()
	if m.membership == nil {
		m.mu.RUnlock()
		return nil, unavailable()
	}
	membership := *m.membership
	m.mu.RUnlock()
	store := state.Store{Acquire: m.DurableStores}
	entries, err := store.List(ctx, "member.")
	if err != nil {
		return nil, err
	}
	members := []domain.HostMembership{{NodeID: membership.NodeID, Version: 1, ClusterID: membership.ClusterID, State: "pending", Epoch: membership.Epoch, BrokerRole: "provisional"}}
	for _, entry := range entries {
		var member Member
		if domain.StrictJSON(entry.Data, &member) != nil {
			return nil, unavailable()
		}
		value := domain.HostMembership{NodeID: member.NodeID, Version: member.Version, ClusterID: membership.ClusterID, State: member.State, Epoch: member.Epoch, BrokerRole: member.BrokerRole}
		if member.NodeID == membership.NodeID {
			members[0] = value
		} else {
			members = append(members, value)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].NodeID < members[j].NodeID })
	return members, nil
}
func (m *Manager) PublishHost(sample domain.HostSample) error {
	m.mu.RLock()
	if m.closed || m.broker == nil || m.membership == nil {
		m.mu.RUnlock()
		return unavailable()
	}
	nc, member := m.broker.Client, *m.membership
	m.mu.RUnlock()
	if sample.HostID != member.NodeID || sample.SchemaVersion != 1 {
		return domain.Err("INVALID_OBSERVATION", "Host observation owner mismatch.", 422)
	}
	b, err := json.Marshal(domain.HostObservation{Epoch: member.Epoch, Sample: sample})
	if err != nil || len(b) > 64<<10 {
		return domain.Err("INVALID_OBSERVATION", "Host observation exceeds bounds.", 422)
	}
	if !nc.IsConnected() {
		return unavailable()
	}
	return nc.Publish("nx."+member.ClusterID+".observation."+member.NodeID, b)
}

// Called while the manager lock is held, alongside its other full-gateway reads.
func (m *Manager) startInventory() error {
	if m.inventorySubscription != nil || m.stores == nil {
		return nil
	}
	prefix := "nx." + m.membership.ClusterID + ".observation."
	sub, err := m.broker.Client.Subscribe(prefix+"*", func(msg *nats.Msg) {
		var observation domain.HostObservation
		if len(msg.Data) > 64<<10 || domain.StrictJSON(msg.Data, &observation) != nil {
			return
		}
		sample := observation.Sample
		if msg.Subject != prefix+sample.HostID || !domain.SafeID.MatchString(sample.HostID) || sample.SchemaVersion != 1 || len(sample.Name) > 256 || len(sample.Endpoints) > 64 || time.Since(sample.ObservedAt) > 45*time.Second || sample.ObservedAt.After(time.Now().Add(5*time.Second)) {
			return
		}
		if !strings.HasSuffix(sample.System, "-linux") {
			return
		}
		member, err := m.member(sample.HostID)
		if err != nil || member.Epoch != observation.Epoch {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		store := state.Store{Acquire: m.DurableStores}
		var previous domain.HostObservation
		rev, err := store.Get(ctx, "observation."+sample.HostID, &previous)
		if err != nil {
			var failure *domain.Error
			if !errors.As(err, &failure) || failure.Code != "NOT_FOUND" {
				return
			}
			rev = 0
		}
		if previous.Epoch > observation.Epoch || (previous.Epoch == observation.Epoch && !sample.ObservedAt.After(previous.Sample.ObservedAt)) {
			return
		}
		if _, err = store.CAS(ctx, "observation."+sample.HostID, observation, rev); err == nil {
			m.mu.RLock()
			svc := m.service
			m.mu.RUnlock()
			if svc != nil {
				svc.NotifyChanges()
			}
		}
	})
	if err != nil {
		return err
	}
	if err = sub.SetPendingLimits(64, 4<<20); err != nil {
		sub.Unsubscribe()
		return err
	}
	m.inventorySubscription = sub
	return nil
}
