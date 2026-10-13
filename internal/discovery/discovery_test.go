package discovery

import (
	"net"
	"testing"
	"time"
)

func TestAdvertisedInterface(t *testing.T) {
	iface, ip, err := advertisedInterface("127.0.0.1:8080")
	if err != nil || iface == nil || !ip.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("loopback selection: %v %v %v", iface, ip, err)
	}
	for _, address := range []string{"bad", "192.0.2.123:8080", "[::1]:8080"} {
		if _, _, err := advertisedInterface(address); err == nil {
			t.Fatalf("accepted non-local IPv4 advertisement %q", address)
		}
	}
}

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
