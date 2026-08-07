package cluster

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

type HostPayload struct {
	NodeID       string        `json:"node_id"`
	Hostname     string        `json:"hostname"`
	NixOSVersion string        `json:"nixos_version"`
	AgentVersion string        `json:"agent_version"`
	Uptime       time.Duration `json:"uptime"`
	PulledSHA    string        `json:"pulled_sha"`
	ApplyRecord  *Record       `json:"apply_record,omitempty"`
	Online       bool          `json:"online"`
}

// View is the in-memory, watcher-fed picture of the cluster's hosts. It is
// never authoritative — the `hosts` KV bucket is.
type View struct {
	mu    sync.RWMutex
	hosts map[string]HostPayload
}

func NewView() *View {
	return &View{hosts: map[string]HostPayload{}}
}

func (v *View) Upsert(p HostPayload) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p.Online = true
	v.hosts[p.NodeID] = p
}

func (v *View) MarkOffline(nodeID string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if p, ok := v.hosts[nodeID]; ok {
		p.Online = false
		v.hosts[nodeID] = p
	}
}

func (v *View) List() []HostPayload {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]HostPayload, 0, len(v.hosts))
	for _, p := range v.hosts {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func (v *View) Count() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.hosts)
}

// Heartbeater publishes this node's HostPayload to hosts.<nodeID> every
// interval; the bucket's TTL expires the key when the node dies, and watchers
// mark it offline. Put failures never crash the daemon — standalone liveness
// outranks cluster visibility.
type Heartbeater struct {
	kv       jetstream.KeyValue
	nodeID   string
	interval time.Duration
	build    func() HostPayload
}

func NewHeartbeater(kv jetstream.KeyValue, nodeID string, interval time.Duration, build func() HostPayload) *Heartbeater {
	return &Heartbeater{kv: kv, nodeID: nodeID, interval: interval, build: build}
}

func (h *Heartbeater) Beat(ctx context.Context) {
	p := h.build()
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	if _, err := h.kv.Put(ctx, "hosts."+h.nodeID, data); err != nil {
		log.Printf("heartbeat put: %v", err)
	}
}

func (h *Heartbeater) Run(ctx context.Context) {
	h.Beat(ctx)
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.Beat(ctx)
		}
	}
}

// WatchHosts feeds the View from KV watch events: puts upsert, deletes (TTL
// expiry or removal) mark the host offline.
func WatchHosts(ctx context.Context, kv jetstream.KeyValue, view *View) error {
	w, err := kv.Watch(ctx, "hosts.*")
	if err != nil {
		return err
	}
	go func() {
		defer w.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-w.Updates():
				if !ok {
					return
				}
				if entry == nil {
					continue
				}
				if entry.Operation() == jetstream.KeyValueDelete || entry.Operation() == jetstream.KeyValuePurge {
					view.MarkOffline(nodeIDFromKey(entry.Key()))
					continue
				}
				var p HostPayload
				if err := json.Unmarshal(entry.Value(), &p); err == nil && p.NodeID != "" {
					view.Upsert(p)
				}
			}
		}
	}()
	return nil
}

func nodeIDFromKey(key string) string {
	const prefix = "hosts."
	if len(key) > len(prefix) && key[:len(prefix)] == prefix {
		return key[len(prefix):]
	}
	return key
}
