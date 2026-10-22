package cluster

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
	"nixmox/internal/telemetry"
)

func TestAuthenticatedLeafTelemetryAndStaleness(t *testing.T) {
	issuer, authority := issuerFixture(t)
	leaf, target := nodeFixture(t)
	joinFixture(t, issuer, authority, leaf, target)
	sampler := telemetry.Start(target.Identity, 250*time.Millisecond, time.Second)
	if err := target.SetTelemetry(sampler); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sampler.Close)
	node := target.Identity().NodeID
	var received domain.HostTelemetry
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		value, err := authority.Telemetry(node)
		if err == nil && value.Sequence >= 2 && value.CPU.Utilization != nil {
			received = value
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if received.Sequence < 2 || received.Freshness != "live" || received.MembershipEpoch == nil || *received.MembershipEpoch != target.Identity().MembershipEpoch {
		t.Fatalf("authenticated sample missing: %+v", received)
	}
	if leaf.telemetry.nc == leaf.broker.Client || leaf.telemetry.nc.Opts.ReconnectBufSize != -1 {
		t.Fatal("activity shares buffered command connection")
	}
	// Pause publication while the agent and its heartbeat/control path remain
	// healthy. The gateway must retain the last sample, then label it stale.
	leaf.telemetry.Close()
	leaf.mu.Lock()
	leaf.telemetry = nil
	leaf.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	before, err := authority.Telemetry(node)
	if err != nil {
		t.Fatal(err)
	}
	forged := before
	forged.Sequence += 100
	epoch := *forged.MembershipEpoch + 1
	forged.MembershipEpoch = &epoch
	data, _ := json.Marshal(forged)
	issuer.telemetry.receive(&nats.Msg{Subject: "nx." + issuer.membership.ClusterID + ".activity." + node, Data: data})
	if value, _ := authority.Telemetry(node); value.Sequence != before.Sequence {
		t.Fatal("obsolete membership accepted")
	}
	forged = before
	forged.Sequence--
	data, _ = json.Marshal(forged)
	issuer.telemetry.receive(&nats.Msg{Subject: "nx." + issuer.membership.ClusterID + ".activity." + node, Data: data})
	if value, _ := authority.Telemetry(node); value.Sequence != before.Sequence {
		t.Fatal("older sequence accepted")
	}
	time.Sleep(3100 * time.Millisecond)
	stale, err := authority.Telemetry(node)
	if err != nil || stale.Freshness != "stale" || stale.Sequence != before.Sequence || *stale.UptimeSeconds != *before.UptimeSeconds {
		t.Fatalf("lost samples did not freeze: %+v %v", stale, err)
	}
	if target.LocalTelemetry().Sequence <= stale.Sequence {
		t.Fatal("transport loss stopped local OS sampling")
	}
	issuer.telemetry.expire(time.Now().Add(6 * time.Minute))
	if value, _ := authority.Telemetry(node); value.Freshness != "unknown" || value.ObservedAt != nil {
		t.Fatal("gateway retained expired samples")
	}
}
