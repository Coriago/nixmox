package cluster

import (
	"context"
	"nixmox/internal/domain"
	"nixmox/internal/state"
	"strings"
	"testing"
	"time"
)

func TestPermanentEvidenceGatesMembershipAndMachineAccess(t *testing.T) {
	m, _ := issuerFixture(t)
	ctx := context.Background()
	id := m.Identity()
	output := "/nix/store/" + strings.Repeat("a", 32) + "-fixture"
	candidate := domain.Candidate{ID: "cand_fixture", OutputPath: output, NarHash: "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", Source: domain.Source{Commit: strings.Repeat("a", 40)}}
	sample := domain.HostSample{HostID: id.NodeID, Environment: "bootstrap", RunningOutput: &output, Freshness: "live", ObservedAt: time.Now()}
	if err := m.VerifyLocalDeployment(ctx, candidate, "adopt_fixture", sample); err == nil {
		t.Fatal("bootstrap evidence promoted member")
	}
	if m.FullMember(ctx, id.NodeID) {
		t.Fatal("creator became full without verification")
	}
	if _, err := m.AuthenticateMachine(ctx, m.keys.Daemon.Leaf, true); err == nil {
		t.Fatal("pending certificate admitted to full route")
	}
	if _, err := m.AuthenticateMachine(ctx, m.keys.Daemon.Leaf, false); err != nil {
		t.Fatal("scoped current certificate rejected", err)
	}
	sample.Environment = "installed"
	sample.ObservedAt = time.Now().Add(-time.Minute)
	if err := m.VerifyLocalDeployment(ctx, candidate, "adopt_fixture", sample); err == nil {
		t.Fatal("stale evidence promoted member")
	}
	sample.ObservedAt = time.Now()
	if err := m.VerifyLocalDeployment(ctx, candidate, "adopt_fixture", sample); err != nil {
		t.Fatal(err)
	}
	if !m.FullMember(ctx, id.NodeID) {
		t.Fatal("verified creator still pending")
	}
	if _, err := m.AuthenticateMachine(ctx, m.keys.Daemon.Leaf, true); err != nil {
		t.Fatal("verified full certificate denied", err)
	}
	st := state.Store{Acquire: m.DurableStores}
	var member Member
	rev, err := st.Get(ctx, "member."+id.NodeID, &member)
	if err != nil {
		t.Fatal(err)
	}
	member.State = "removed"
	member.Version++
	if _, err = st.CAS(ctx, "member."+id.NodeID, member, rev); err != nil {
		t.Fatal(err)
	}
	if _, err = m.AuthenticateMachine(ctx, m.keys.Daemon.Leaf, false); err == nil {
		t.Fatal("removed member retained machine access")
	}
}
