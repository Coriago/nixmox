package cluster

import (
	"context"
	"log"
	"sync"
	"time"

	"nixmox/internal/natsx"
)

const (
	hostsBucketTTL   = 30 * time.Second
	heartbeatEvery   = 10 * time.Second
	replicaScanEvery = time.Minute
)

// Runtime owns the cluster-dependent subsystems (heartbeat, host watcher,
// KV-backed record store). It is re-armed after every NATS reconfigure:
// cancel the old generation, ensure buckets on the new embedded server,
// start fresh goroutines. Anything cluster-scoped lives behind this latch.
type Runtime struct {
	View      *View
	Switch    *SwitchingRecordStore
	payloadFn func() HostPayload

	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewRuntime(view *View, sw *SwitchingRecordStore, payloadFn func() HostPayload) *Runtime {
	return &Runtime{View: view, Switch: sw, payloadFn: payloadFn}
}

func (rt *Runtime) Rearm(ctx context.Context, emb *natsx.EmbeddedServer) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if rt.cancel != nil {
		rt.cancel()
		rt.cancel = nil
	}
	if emb == nil {
		return
	}
	rtCtx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel

	hostsKV, err := natsx.EnsureKV(ctx, emb.JS, "hosts", natsx.BucketConfig{TTL: hostsBucketTTL})
	if err != nil {
		log.Printf("cluster runtime: hosts bucket: %v", err)
		return
	}
	applylogKV, err := natsx.EnsureKV(ctx, emb.JS, "applylog", natsx.BucketConfig{})
	if err != nil {
		log.Printf("cluster runtime: applylog bucket: %v", err)
		return
	}

	hb := NewHeartbeater(hostsKV, rt.payloadFn().NodeID, heartbeatEvery, rt.payloadFn)
	go hb.Run(rtCtx)
	if err := WatchHosts(rtCtx, hostsKV, rt.View); err != nil {
		log.Printf("cluster runtime: watch hosts: %v", err)
	}
	rt.Switch.SetRemote(NewKVRecordStore(applylogKV))
	log.Printf("cluster runtime: armed (heartbeat every %s, ttl %s)", heartbeatEvery, hostsBucketTTL)
}

func (rt *Runtime) Stop() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.cancel != nil {
		rt.cancel()
		rt.cancel = nil
	}
}
