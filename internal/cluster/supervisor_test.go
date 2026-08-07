package cluster

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nixmox/internal/config"
)

func testPorts(t *testing.T, n int) []int {
	t.Helper()
	var out []int
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ln.Addr().(*net.TCPAddr).Port)
		ln.Close()
	}
	return out
}

func testSupervisor(t *testing.T, hostname string, clientPort, clusterPort int) *Supervisor {
	t.Helper()
	cfg := &config.Config{
		Hostname:    hostname,
		NATSPort:    clientPort,
		ClusterPort: clusterPort,
	}
	return NewSupervisor(cfg, t.TempDir(), "127.0.0.1")
}

func TestSupervisorStandaloneThenCreate(t *testing.T) {
	ports := testPorts(t, 2)
	sup := testSupervisor(t, "node1", ports[0], ports[1])
	ctx := context.Background()

	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sup.Stop()

	if st := sup.State(0); st.Status != "unclustered" {
		t.Fatalf("fresh start should be unclustered, got %+v", st)
	}

	if err := sup.CreateCluster(ctx, "homelab"); err != nil {
		t.Fatalf("CreateCluster: %v", err)
	}
	st := sup.State(1)
	if st.Status != "clustered" || st.ClusterName != "homelab" {
		t.Errorf("after create: %+v", st)
	}

	if err := sup.CreateCluster(ctx, "other"); err == nil {
		t.Error("second create must fail (no renames)")
	}
}

func TestSupervisorTwoNodeJoin(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: two embedded NATS servers")
	}
	ports := testPorts(t, 4)
	ctx := context.Background()

	seed := testSupervisor(t, "node1", ports[0], ports[1])
	if err := seed.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer seed.Stop()
	if err := seed.CreateCluster(ctx, "homelab"); err != nil {
		t.Fatal(err)
	}

	seedHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/bootstrap":
			json.NewEncoder(w).Encode(seed.State(1))
		case "/api/v1/cluster/join":
			var req JoinRequest
			json.NewDecoder(r.Body).Decode(&req)
			resp, err := seed.AcceptJoin(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer seedHTTP.Close()
	seedAddr := seedHTTP.Listener.Addr().String()

	joiner := testSupervisor(t, "node2", ports[2], ports[3])
	if err := joiner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer joiner.Stop()

	if err := joiner.JoinCluster(ctx, seedAddr); err != nil {
		t.Fatalf("JoinCluster: %v", err)
	}
	if st := joiner.State(2); st.Status != "clustered" || st.ClusterName != "homelab" {
		t.Errorf("joiner state: %+v", st)
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		routes := seed.Embedded().Srv.NumRoutes()
		leader := seed.Embedded().Srv.JetStreamIsLeader()
		peers := seed.Embedded().Srv.JetStreamClusterPeers()
		t.Logf("seed: routes=%d leader=%v peers=%v", routes, leader, peers)
		if routes >= 2 && len(peers) >= 1 {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("mesh never formed (see route/leader log lines above)")
}

func TestJoinNameMismatch(t *testing.T) {
	ports := testPorts(t, 2)
	sup := testSupervisor(t, "node1", ports[0], ports[1])
	sup.ident.ClusterName = "homelab"

	_, err := sup.AcceptJoin(JoinRequest{NodeID: "node2", ClusterAddr: "nats://127.0.0.1:1", TargetClusterName: "different"})
	if err != ErrClusterNameMismatch {
		t.Errorf("want ErrClusterNameMismatch, got %v", err)
	}

	sup2 := testSupervisor(t, "node3", testPorts(t, 2)[0], testPorts(t, 2)[1])
	if _, err := sup2.AcceptJoin(JoinRequest{TargetClusterName: "x"}); err != ErrNotClustered {
		t.Errorf("unclustered peer: want ErrNotClustered, got %v", err)
	}

	if _, err := sup.AcceptJoin(JoinRequest{NodeID: "n", TargetClusterName: "homelab"}); err == nil {
		t.Error("join without cluster_addr must be rejected")
	}
}
