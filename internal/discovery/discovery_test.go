package discovery

import (
	"testing"
	"time"
)

func TestRegistryExpiryAndOrder(t *testing.T) {
	r := New()
	r.peers["old"] = Peer{Address: "old", Seen: time.Now().Add(-time.Minute)}
	r.peers["b"] = Peer{Address: "b", Seen: time.Now()}
	r.peers["a"] = Peer{Address: "a", Seen: time.Now()}
	peers := r.List()
	if len(peers) != 2 || peers[0].Address != "a" {
		t.Fatal(peers)
	}
	if _, ok := r.peers["old"]; ok {
		t.Fatal("expired peer retained")
	}
}
