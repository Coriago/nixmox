package network

import (
	"context"
	"net"
	"nixmox/internal/config"
	"testing"
)

func TestManagementPolicy(t *testing.T) {
	interfaces := []Interface{{Name: "lo", Up: true, Loopback: true, Addresses: []net.IP{net.ParseIP("127.0.0.1")}}, {Name: "wan", Up: true, Addresses: []net.IP{net.ParseIP("192.0.2.10")}}, {Name: "wifi", Up: true, Addresses: []net.IP{net.ParseIP("fe80::1"), net.ParseIP("10.0.0.2")}}}
	c := config.Default()
	inspect := Inspector{Config: c, Interfaces: func() ([]Interface, error) { return interfaces, nil }}
	n, err := inspect.Inspect(context.Background(), "n_a")
	if err != nil || n.Network.Status != "disconnected" {
		t.Fatal(n, err)
	}
	c.Network.ManagementInterfaces = []string{"wifi"}
	inspect.Config = c
	n, err = inspect.Inspect(context.Background(), "n_a")
	if err != nil || n.Network.Status != "ready" || *n.Network.Address != "10.0.0.2" || *n.ConsoleURL != "http://10.0.0.2:63170" {
		t.Fatal(n, err)
	}
	c.Network.PreferredInterfaces = []string{"missing"}
	c.Network.AllowFallback = false
	inspect.Config = c
	n, _ = inspect.Inspect(context.Background(), "n_a")
	if n.Network.Status == "ready" {
		t.Fatal("fallback ignored policy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inspect.Inspect(ctx, "n_a"); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestRealOSInspection(t *testing.T) {
	interfaces, err := SystemInterfaces()
	if err != nil || len(interfaces) == 0 {
		t.Fatal(interfaces, err)
	}
	c := config.Default()
	for _, i := range interfaces {
		if !i.Loopback {
			c.Network.ManagementInterfaces = append(c.Network.ManagementInterfaces, i.Name)
		}
	}
	n, err := (Inspector{Config: c}).Inspect(context.Background(), "n_real")
	if err != nil || n.Freshness != "live" || n.Network.CheckedAt == nil {
		t.Fatal(n, err)
	}
	t.Logf("real OS observed %d interfaces: %s", len(interfaces), n.Network.Status)
}
