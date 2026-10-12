package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"nixmox/internal/natsx"
	"testing"
	"time"
)

func TestLeaveAndRejoin(t *testing.T) {
	if testing.Short() {
		t.Skip("embedded NATS integration")
	}
	ports := testPorts(t, 4)
	ctx := context.Background()
	seed := testSupervisor(t, "seed", ports[0], ports[1])
	joiner := testSupervisor(t, "joining", ports[2], ports[3])
	for _, s := range []*Supervisor{seed, joiner} {
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer s.Stop()
	}
	if err := seed.CreateCluster(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/bootstrap" {
			json.NewEncoder(w).Encode(seed.State(1))
			return
		}
		var req JoinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		res, err := seed.AcceptJoin(req)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(res)
	}))
	defer api.Close()
	for i := 0; i < 2; i++ {
		if err := joiner.JoinCluster(ctx, api.URL); err != nil {
			t.Fatal(err)
		}
		if err := natsx.WaitReady(seed.Embedded().JS, 15*time.Second); err != nil {
			t.Fatal(err)
		}
		// Exercise removal of the metadata leader, not just a follower.
		if seed.Embedded().Srv.JetStreamIsLeader() {
			if _, err := seed.Embedded().Sys.Request("$JS.API.META.LEADER.STEPDOWN", nil, 5*time.Second); err != nil {
				t.Fatal(err)
			}
		}
		deadline := time.Now().Add(10 * time.Second)
		for !joiner.Embedded().Srv.JetStreamIsLeader() && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if !joiner.Embedded().Srv.JetStreamIsLeader() {
			t.Fatal("joiner never became leader")
		}
		route := joiner.State(2).NATSRoute
		if err := joiner.Leave(ctx); err != nil {
			t.Fatalf("leave cycle %d: %v", i, err)
		}
		if joiner.AutoJoinAllowed() {
			t.Fatal("explicitly removed node must not autojoin")
		}
		if got := joiner.State(0).Status; got != "unclustered" {
			t.Fatal(got)
		}
		if err := seed.ForgetRoute(ctx, route); err != nil {
			t.Fatal(err)
		}
		if len(seed.ident.Routes) != 0 {
			t.Fatal(seed.ident.Routes)
		}
		if err := natsx.WaitReady(seed.Embedded().JS, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
}
func TestInventorySurvivesOffline(t *testing.T) {
	v := NewView()
	v.Upsert(HostPayload{NodeID: "node"})
	path := t.TempDir() + "/hosts.json"
	if err := v.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded := NewView()
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if rows := loaded.List(); len(rows) != 1 || rows[0].Online {
		t.Fatal(rows)
	}
	loaded.Remove("node")
	if loaded.Count() != 0 {
		t.Fatal("remove did not shrink inventory")
	}
}
