package discovery

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

func TestDiscoveryReconcilesAllInterfaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var endpoints []endpoint
	started := make(chan string, 32)
	stopped := make(chan string, 32)
	scan := func() ([]endpoint, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]endpoint{}, endpoints...), nil
	}
	serve := func(ctx context.Context, _ func() Peer, e endpoint) error {
		started <- e.iface.Name
		if e.iface.Name == "vpn" {
			return fmt.Errorf("multicast unavailable")
		}
		<-ctx.Done()
		stopped <- e.iface.Name
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- New().run(ctx, func() Peer { return Peer{Address: "0.0.0.0:8080"} }, scan, serve, 10*time.Millisecond)
	}()
	wait := func(ch <-chan string, name string) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case got := <-ch:
				if got == name {
					return
				}
			case <-timer.C:
				t.Fatalf("did not observe %s", name)
			}
		}
	}
	// No address at boot, then both a failing VPN and working LAN appear.
	mu.Lock()
	endpoints = []endpoint{{iface: net.Interface{Index: 1, Name: "vpn"}, ip: net.ParseIP("100.64.0.1")}, {iface: net.Interface{Index: 2, Name: "wifi"}, ip: net.ParseIP("192.168.1.2")}}
	mu.Unlock()
	wait(started, "wifi")
	wait(started, "vpn")
	mu.Lock()
	endpoints = nil
	mu.Unlock()
	wait(stopped, "wifi")
	mu.Lock()
	endpoints = []endpoint{{iface: net.Interface{Index: 2, Name: "wifi"}, ip: net.ParseIP("192.168.1.3")}}
	mu.Unlock()
	wait(started, "wifi")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("discovery did not shut down")
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

// Optional hardware check: receive only, without advertising a synthetic node.
func TestDiscoveryLANReception(t *testing.T) {
	target := os.Getenv("NIXMOX_DISCOVERY_LIVE_TARGET")
	if target == "" {
		t.Skip("set NIXMOX_DISCOVERY_LIVE_TARGET for a live LAN receiver check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	r := New()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, func() Peer { return Peer{NodeID: "receive-only-check"} }) }()
	defer func() { cancel(); <-done }()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		for _, p := range r.List() {
			if p.Address == target && p.Installer {
				t.Logf("received installer %s at %s", p.NodeID, p.Address)
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no installer announcement from %s", target)
		case <-tick.C:
		}
	}
}
