package cluster

import (
	"context"
	"nixmox/internal/events"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/natsx"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeploymentChangesReachKVWatchBeforeHeartbeat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts, err := natsx.NewOptions("event-test", 0, 0, "127.0.0.1", t.TempDir(), natsx.ClusterSpec{})
	if err != nil {
		t.Fatal(err)
	}
	emb, err := natsx.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer emb.Stop()
	kv, err := natsx.EnsureKV(ctx, emb.JS, "hosts", natsx.BucketConfig{})
	if err != nil {
		t.Fatal(err)
	}
	updates, changes := &events.Signal{}, &events.Signal{}
	view := NewView()
	view.Changed = updates.Notify
	var active atomic.Bool
	heartbeat := NewHeartbeater(kv, "host", time.Hour, func() HostPayload {
		p := HostPayload{NodeID: "host"}
		if active.Load() {
			p.ActiveDeployment = &jobs.Job{ID: "apply", State: "building"}
		}
		return p
	})
	heartbeat.Changes = changes
	go heartbeat.Run(ctx)
	go WatchHosts(ctx, kv, view)
	deadline := time.After(3 * time.Second)
	for view.Count() == 0 {
		next := updates.Watch()
		if view.Count() > 0 {
			break
		}
		select {
		case <-next:
		case <-deadline:
			t.Fatal("initial snapshot missing")
		}
	}
	active.Store(true)
	changes.Notify()
	for {
		next := updates.Watch()
		list := view.List()
		if len(list) == 1 && list[0].ActiveDeployment != nil {
			return
		}
		select {
		case <-next:
		case <-deadline:
			t.Fatal("change waited for the hourly heartbeat")
		}
	}
}

func TestTelemetryHasIndependentCadenceAndDoesNotRenewLiveness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	opts, err := natsx.NewOptions("telemetry-test", 0, 0, "127.0.0.1", t.TempDir(), natsx.ClusterSpec{})
	if err != nil {
		t.Fatal(err)
	}
	emb, err := natsx.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer emb.Stop()
	view := NewView()
	view.Upsert(HostPayload{NodeID: "host", Hostname: "host"})
	view.mu.Lock()
	originalSeen := time.Now().Add(-time.Minute)
	view.seen["host"] = originalSeen
	view.mu.Unlock()
	var samples atomic.Int32
	updates := &events.Signal{}
	view.Changed = updates.Notify
	rt := &Runtime{View: view, Telemetry: func() hostinfo.HostInfo {
		n := samples.Add(1)
		return hostinfo.HostInfo{Hostname: "host", Uptime: time.Duration(n) * time.Second, Resources: hostinfo.Resources{MemoryAvailable: uint64(n)}}
	}}
	done := make(chan struct{})
	go func() { rt.runTelemetry(ctx, emb); close(done) }()
	defer func() { cancel(); <-done }()
	for {
		next := updates.Watch()
		rows := view.List()
		if rows[0].Resources.MemoryAvailable >= 3 {
			if rows[0].Online {
				t.Fatal("telemetry renewed expired host liveness")
			}
			view.mu.RLock()
			seen := view.seen["host"]
			view.mu.RUnlock()
			if !seen.Equal(originalSeen) {
				t.Fatal("telemetry changed heartbeat timestamp")
			}
			return
		}
		select {
		case <-next:
		case <-ctx.Done():
			t.Fatal("telemetry waited for heartbeat")
		}
	}
}
