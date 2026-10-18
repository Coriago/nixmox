package cluster

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/events"
	"nixmox/internal/hostinfo"
	"nixmox/internal/messaging"
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
	Changes          *events.Signal
	Telemetry        func() hostinfo.HostInfo
	TelemetryChanged func()
	View             *View
	Switch           *SwitchingRecordStore
	payloadFn        func() HostPayload

	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped bool
}

func NewRuntime(view *View, sw *SwitchingRecordStore, payloadFn func() HostPayload) *Runtime {
	return &Runtime{View: view, Switch: sw, payloadFn: payloadFn}
}

func (rt *Runtime) Rearm(ctx context.Context, emb *natsx.EmbeddedServer) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.stopped || ctx.Err() != nil {
		return
	}

	if rt.cancel != nil {
		rt.cancel()
		rt.cancel = nil
	}
	rt.Switch.SetRemote(nil)
	if emb == nil {
		return
	}
	rtCtx, cancel := context.WithCancel(ctx)
	rt.cancel = cancel

	go rt.arm(rtCtx, emb)
}

func (rt *Runtime) arm(rtCtx context.Context, emb *natsx.EmbeddedServer) {
	go rt.runTelemetry(rtCtx, emb)
	var hostsKV, applylogKV jetstream.KeyValue
	for {
		var err error
		hostsKV, err = natsx.EnsureKV(rtCtx, emb.JS, "hosts", natsx.BucketConfig{TTL: hostsBucketTTL})
		if err == nil {
			applylogKV, err = natsx.EnsureKV(rtCtx, emb.JS, "applylog", natsx.BucketConfig{})
		}
		if err == nil {
			break
		}
		log.Printf("cluster runtime: waiting for buckets: %v", err)
		if !retryRuntime(rtCtx) {
			return
		}
	}
	rt.mu.Lock()
	if rt.stopped || rtCtx.Err() != nil {
		rt.mu.Unlock()
		return
	}
	rt.Switch.SetRemote(NewKVRecordStore(applylogKV))
	rt.mu.Unlock()

	hb := NewHeartbeater(hostsKV, rt.payloadFn().NodeID, heartbeatEvery, rt.payloadFn)
	hb.Changes = rt.Changes
	go hb.Run(rtCtx)
	go func() {
		scaler := ReplicaScaler{Buckets: []string{"KV_hosts", "KV_applylog", messaging.Stream}, PeerCount: func() int {
			n := 0
			for _, h := range rt.View.List() {
				if h.Online && !h.Temporary {
					n++
				}
			}
			return n
		}, Scale: func(ctx context.Context, bucket string, n int) error {
			return natsx.ScaleStream(ctx, emb.JS, bucket, n)
		}}
		tick := time.NewTicker(replicaScanEvery)
		defer tick.Stop()
		for {
			select {
			case <-rtCtx.Done():
				return
			case <-tick.C:
				scaler.Check(rtCtx)
			}
		}
	}()
	log.Printf("cluster runtime: armed (heartbeat every %s, ttl %s)", heartbeatEvery, hostsBucketTTL)
	for rtCtx.Err() == nil {
		// Account readiness can precede stream leader election. A failed initial
		// watch or a closed watch must not leave a stale view forever.
		if err := WatchHosts(rtCtx, hostsKV, rt.View); err != nil && rtCtx.Err() == nil {
			log.Printf("cluster runtime: retrying host watch: %v", err)
		}
		if !retryRuntime(rtCtx) {
			return
		}
	}
}

func retryRuntime(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Second):
		return true
	}
}

func (rt *Runtime) Stop() {
	rt.mu.Lock()
	rt.stopped = true
	rt.Switch.SetRemote(nil)
	defer rt.mu.Unlock()
	if rt.cancel != nil {
		rt.cancel()
		rt.cancel = nil
	}
}
