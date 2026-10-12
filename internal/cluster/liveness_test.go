package cluster

import (
	"testing"
	"time"
)

func TestLivenessExpiresWithoutKVDelete(t *testing.T) {
	v := NewView()
	v.Upsert(HostPayload{NodeID: "node"})
	if !v.List()[0].Online {
		t.Fatal("fresh heartbeat offline")
	}
	v.seen["node"] = time.Now().Add(-hostsBucketTTL - time.Second)
	if v.List()[0].Online {
		t.Fatal("stale heartbeat still online")
	}
	v.Upsert(HostPayload{NodeID: "node"})
	if !v.List()[0].Online {
		t.Fatal("recovery did not restore online state")
	}
}
