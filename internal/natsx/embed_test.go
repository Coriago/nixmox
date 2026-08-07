package natsx

import (
	"context"
	"testing"
	"time"
)

func freePorts(t *testing.T) (client, cluster int) {
	t.Helper()
	addrs := reserveAddrs(t, 2)
	return addrs[0], addrs[1]
}

func TestEmbeddedStandalone(t *testing.T) {
	clientPort, _ := freePorts(t)
	opts, err := NewOptions("testnode", clientPort, 16222, "127.0.0.1", t.TempDir(), ClusterSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Cluster.Name != "" {
		t.Errorf("standalone mode must have no cluster name, got %q", opts.Cluster.Name)
	}

	emb, err := Start(opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer emb.Stop()

	if err := WaitReady(emb.JS, 15*time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	kv, err := EnsureKV(ctx, emb.JS, "hosts", BucketConfig{TTL: time.Minute})
	if err != nil {
		t.Fatalf("EnsureKV (meta-leader readiness): %v", err)
	}
	if _, err := kv.Put(ctx, "hosts.a", []byte("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entry, err := kv.Get(ctx, "hosts.a")
	if err != nil || string(entry.Value()) != "1" {
		t.Fatalf("Get: %v %v", entry, err)
	}

	again, err := EnsureKV(ctx, emb.JS, "hosts", BucketConfig{TTL: time.Minute})
	if err != nil {
		t.Fatalf("EnsureKV idempotent: %v", err)
	}
	if again == nil {
		t.Error("EnsureKV returned nil on second call")
	}
}

func TestScaleReplicasRejectsR2(t *testing.T) {
	clientPort, _ := freePorts(t)
	opts, _ := NewOptions("testnode", clientPort, 16223, "127.0.0.1", t.TempDir(), ClusterSpec{})
	emb, err := Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer emb.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := EnsureKV(ctx, emb.JS, "hosts", BucketConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := ScaleReplicas(ctx, emb.JS, "hosts", 2); err == nil {
		t.Fatal("R=2 must be rejected")
	}
	if err := ScaleReplicas(ctx, emb.JS, "hosts", 1); err != nil {
		t.Fatalf("R=1 to R=1 should be a no-op, got %v", err)
	}
}
