package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/events"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
)

type HostPayload struct {
	Temporary         bool               `json:"temporary,omitempty"`
	AgeRecipient      string             `json:"age_recipient,omitempty"`
	AgeRecipientError string             `json:"age_recipient_error,omitempty"`
	ObservedAt        time.Time          `json:"-"`
	ActiveDeployment  *jobs.Job          `json:"active_deployment,omitempty"`
	StatusUnavailable bool               `json:"status_unavailable,omitempty"`
	Kernel            string             `json:"kernel"`
	Resources         hostinfo.Resources `json:"resources"`
	HTTPAddress       string             `json:"http_address"`
	ClusterRoute      string             `json:"cluster_route"`
	NodeID            string             `json:"node_id"`
	Hostname          string             `json:"hostname"`
	NixOSVersion      string             `json:"nixos_version"`
	AgentVersion      string             `json:"agent_version"`
	Uptime            time.Duration      `json:"uptime"`
	RemoteSHA         string             `json:"remote_sha"`
	PulledSHA         string             `json:"pulled_sha"`
	ApplyRecord       *Record            `json:"apply_record,omitempty"`
	Online            bool               `json:"online"`
}

func (p HostPayload) Info() hostinfo.HostInfo {
	return hostinfo.HostInfo{AgeRecipient: p.AgeRecipient, AgeRecipientError: p.AgeRecipientError, Hostname: p.Hostname, NixOSVersion: p.NixOSVersion, AgentVersion: p.AgentVersion, Uptime: p.Uptime, Resources: p.Resources, Kernel: p.Kernel}
}

// View is the in-memory, watcher-fed picture of the cluster's hosts. It is
// never authoritative — the `hosts` KV bucket is.
type View struct {
	Changed   func()
	mu        sync.RWMutex
	hosts     map[string]HostPayload
	telemetry map[string]telemetrySample
	seen      map[string]time.Time
}

func NewView() *View {
	return &View{hosts: map[string]HostPayload{}, seen: map[string]time.Time{}}
}

func (v *View) Upsert(p HostPayload) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p.Online = true
	v.hosts[p.NodeID] = p
	v.seen[p.NodeID] = time.Now()
	if v.Changed != nil {
		v.Changed()
	}
}

func (v *View) MarkOffline(nodeID string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if p, ok := v.hosts[nodeID]; ok {
		p.Online = false
		v.hosts[nodeID] = p
		if v.Changed != nil {
			v.Changed()
		}
	}
}

func (v *View) List() []HostPayload {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]HostPayload, 0, len(v.hosts))
	for id, p := range v.hosts {
		p.ObservedAt = v.seen[id]
		if sample, ok := v.telemetry[id]; ok && sample.observed.After(p.ObservedAt) {
			p.Resources, p.Uptime, p.ObservedAt = sample.info.Resources, sample.info.Uptime, sample.observed
		}
		// MaxAge expiry need not emit a KV deletion. Liveness also expires locally.
		if p.Online && time.Since(v.seen[id]) > hostsBucketTTL {
			p.Online = false
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func (v *View) Count() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	n := 0
	for _, h := range v.hosts {
		if !h.Temporary {
			n++
		}
	}
	return n
}

// Heartbeater publishes this node's HostPayload to hosts.<nodeID> every
// interval; the bucket's TTL expires the key when the node dies, and watchers
// mark it offline. Put failures never crash the daemon — standalone liveness
// outranks cluster visibility.
type Heartbeater struct {
	Changes  *events.Signal
	kv       jetstream.KeyValue
	nodeID   string
	interval time.Duration
	build    func() HostPayload
}

func NewHeartbeater(kv jetstream.KeyValue, nodeID string, interval time.Duration, build func() HostPayload) *Heartbeater {
	return &Heartbeater{kv: kv, nodeID: nodeID, interval: interval, build: build}
}

func (h *Heartbeater) Beat(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
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
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		var changed <-chan struct{}
		if h.Changes != nil {
			changed = h.Changes.Watch()
		}
		h.Beat(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-changed:
			// Coalesce chatty build logs while preserving the latest snapshot.
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}

// WatchHosts feeds the View from KV watch events: puts upsert, deletes (TTL
// expiry or removal) mark the host offline. Blocks until cancellation or watch
// failure; the runtime retries failures, including initial consumer creation.
func WatchHosts(ctx context.Context, kv jetstream.KeyValue, view *View) error {
	w, err := kv.Watch(ctx, "hosts.>")
	if err != nil {
		return err
	}
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case entry, ok := <-w.Updates():
			if !ok {
				return fmt.Errorf("host watch closed")
			}
			if entry == nil {
				continue
			}
			if entry.Operation() == jetstream.KeyValuePurge {
				view.Remove(nodeIDFromKey(entry.Key()))
				continue
			}
			if entry.Operation() == jetstream.KeyValueDelete {
				view.MarkOffline(nodeIDFromKey(entry.Key()))
				continue
			}
			var p HostPayload
			if err := json.Unmarshal(entry.Value(), &p); err == nil && p.NodeID != "" {
				view.Upsert(p)
			}
		}
	}
}

func nodeIDFromKey(key string) string {
	const prefix = "hosts."
	if len(key) > len(prefix) && key[:len(prefix)] == prefix {
		return key[len(prefix):]
	}
	return key
}
