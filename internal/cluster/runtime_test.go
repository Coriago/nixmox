package cluster

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/natsx"
)

type recoveringKV struct {
	jetstream.KeyValue
	watches atomic.Int32
}

func (kv *recoveringKV) Watch(ctx context.Context, keys string, opts ...jetstream.WatchOpt) (jetstream.KeyWatcher, error) {
	if kv.watches.Add(1) == 1 {
		return nil, context.DeadlineExceeded
	}
	return kv.KeyValue.Watch(ctx, keys, opts...)
}

type recoveringJS struct {
	jetstream.JetStream
	hosts   *recoveringKV
	lookups atomic.Int32
}

func (js *recoveringJS) KeyValue(ctx context.Context, bucket string) (jetstream.KeyValue, error) {
	if bucket == "hosts" {
		if js.lookups.Add(1) == 1 {
			return nil, context.DeadlineExceeded
		}
		return js.hosts, nil
	}
	return js.JetStream.KeyValue(ctx, bucket)
}

// Regression for VM recovery: account readiness may precede bucket availability
// and consumer creation. Neither transient failure may permanently disable the view.
func TestRuntimeRecoversBucketAndWatchInitialization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts, err := natsx.NewOptions("runtime", 0, 0, "127.0.0.1", t.TempDir(), natsx.ClusterSpec{})
	if err != nil {
		t.Fatal(err)
	}
	emb, err := natsx.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer emb.Stop()
	hosts, err := natsx.EnsureKV(ctx, emb.JS, "hosts", natsx.BucketConfig{})
	if err != nil {
		t.Fatal(err)
	}
	js := &recoveringJS{JetStream: emb.JS, hosts: &recoveringKV{KeyValue: hosts}}
	view := NewView()
	local, err := NewLocalRecordStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	records := NewSwitchingRecordStore(local)
	runtime := NewRuntime(view, records, func() HostPayload { return HostPayload{NodeID: "self"} })
	defer runtime.Stop()
	runtime.Rearm(ctx, &natsx.EmbeddedServer{JS: js})
	if _, err := hosts.Put(ctx, "hosts.peer", []byte(`{"node_id":"peer","uptime":123}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for view.Count() != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if view.Count() != 2 || js.lookups.Load() < 2 || js.hosts.watches.Load() < 2 {
		t.Fatalf("recovery did not restore inventory: %+v (lookups=%d watches=%d)", view.List(), js.lookups.Load(), js.hosts.watches.Load())
	}
	runtime.Stop()
	if records.remoteOrNil() != nil {
		t.Fatal("stopped runtime retained remote store")
	}
	runtime.Rearm(ctx, emb)
	if records.remoteOrNil() != nil {
		t.Fatal("stopped runtime rearmed")
	}
}
