package server

import (
	"context"
	"testing"

	"nixmox/internal/cluster"
)

func TestAddDiscoveredNodeRequiresClusterAndConfirmedFreshHint(t *testing.T) {
	s := operationServer(t)
	supervisor := cluster.NewSupervisor(s.Config, s.Config.DataDir, "127.0.0.1")
	s.Cluster = supervisor
	path := "/api/v1/cluster/add"
	if w := call(s, "POST", path, `{}`); w.Code != 409 {
		t.Fatalf("standalone add: %d", w.Code)
	}
	if err := supervisor.CreateCluster(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{}`, `{"node_id":"peer","confirm":"wrong"}`,
		`{"node_id":"local","confirm":"local"}`,
	} {
		if w := call(s, "POST", path, body); w.Code != 400 {
			t.Fatalf("unconfirmed selection %s: %d", body, w.Code)
		}
	}
	// No outbound request should be made for an arbitrary/expired address.
	if w := call(s, "POST", path, `{"node_id":"peer","confirm":"peer","address":"127.0.0.1:1"}`); w.Code != 409 {
		t.Fatalf("undiscovered address: %d %s", w.Code, w.Body.String())
	}
	if w := call(s, "GET", "/api/v1/cluster/diagnostics", ""); w.Code != 503 {
		t.Fatalf("unavailable embedded server: %d", w.Code)
	}
}
