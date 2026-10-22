package cluster

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/network"
	"nixmox/internal/service"
	"nixmox/internal/storage"
)

func TestCreateCommitsGenesisAndPreservesIdentityAcrossRestart(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := config.Default()
	c.DataDir = st.Root
	c.NATS.ClientPort = -1
	c.NATS.RoutePort = -1
	c.NATS.LeafPort = -1
	m, err := Open(st, c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := service.Open(st, network.Inspector{Config: c}, m.Operations()...)
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	identity := s.Identity()
	command := domain.Command{ID: "cmd_genesis", Type: "cluster.create", TargetID: identity.NodeID, Payload: json.RawMessage(`{"name":"homelab"}`)}
	r, err := s.Admit(command)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var task domain.Task
	for time.Now().Before(deadline) {
		task, err = s.Task(r.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if task.State != "succeeded" {
		s.Close()
		m.Close()
		t.Fatalf("genesis: %+v", task)
	}
	joined := s.Identity()
	if joined.ClusterID == nil || joined.NodeID != identity.NodeID || joined.PublicKey != identity.PublicKey || joined.MembershipEpoch != 1 {
		t.Fatal("identity continuity failed", joined)
	}
	entry, err := m.stores.State.Get("cluster")
	if err != nil {
		t.Fatal(err)
	}
	var g Genesis
	if json.Unmarshal(entry.Value(), &g) != nil || g.ID != *joined.ClusterID || g.Creator.Membership != "pending" || g.Replicas != 1 {
		t.Fatalf("invalid genesis %s", entry.Value())
	}
	s.Close()
	m.Close()
	m, err = Open(st, c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s, err = service.Open(st, network.Inspector{Config: c}, m.Operations()...)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replayed, err := s.Admit(command)
	if err != nil || replayed.TaskID != r.TaskID {
		t.Fatal("lost genesis receipt", replayed, err)
	}
	current := s.Identity()
	if current.ClusterID == nil || *current.ClusterID != *joined.ClusterID || current.KeyID != joined.KeyID {
		t.Fatal("changed cluster on restart")
	}
	command.ID = "cmd_replace"
	if _, err = s.Admit(command); err == nil {
		t.Fatal("joined node accepted replacement genesis")
	}
	if _, err = m.Status(); err != nil {
		t.Fatal(err)
	}
}
