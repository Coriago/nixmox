package server

import (
	"nixmox/internal/cluster"
	"nixmox/internal/jobs"
	"strings"
	"testing"
)

type statusCluster struct{ ClusterAPI }

func (statusCluster) State(int) cluster.ClusterState {
	return cluster.ClusterState{Status: "clustered"}
}
func TestHostStatusUsesWatchedDeployment(t *testing.T) {
	s := operationServer(t)
	s.Cluster = statusCluster{}
	s.View.Upsert(cluster.HostPayload{NodeID: "local", Hostname: "local"})
	peer := cluster.HostPayload{NodeID: "remote", Hostname: "remote", PulledSHA: "new", ActiveDeployment: &jobs.Job{ID: "deploy", State: "building", Phase: "Activating system"}}
	s.View.Upsert(peer)
	for _, path := range []string{"/partials/hosts", "/partials/sidebar"} {
		body := call(s, "GET", path, "").Body.String()
		for _, want := range []string{"remote", "Deploying", "spinner", "Activating system"} {
			if !strings.Contains(body, want) {
				t.Fatal(path, want, body)
			}
		}
	}
	peer.ActiveDeployment = nil
	peer.ApplyRecord = &cluster.Record{GitSHA: "new", Status: "success", Mode: "switch"}
	s.View.Upsert(peer)
	body := call(s, "GET", "/partials/hosts", "").Body.String()
	if strings.Contains(body, "Deploying") || !strings.Contains(body, "Reapply") {
		t.Fatal(body)
	}
	s.View.MarkOffline("remote")
	body = call(s, "GET", "/partials/sidebar", "").Body.String()
	if !strings.Contains(body, "Unknown · offline") || strings.Contains(body, "Deploying") {
		t.Fatal(body)
	}
}
