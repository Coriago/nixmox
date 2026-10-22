package broker

import (
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"nixmox/internal/pki"
	"nixmox/internal/storage"
)

func TestPendingLeafPermissionsAtAcceptingHub(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "issuer"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := pki.OpenAuthority(st, "c_leaf", true)
	if err != nil {
		t.Fatal(err)
	}
	hubCfg, leafCfg := node(t), node(t)
	for _, c := range []*Config{&hubCfg, &leafCfg} {
		c.ClusterID = "c_leaf"
		c.Roots = pki.Pool(a.Certificate)
		c.Keys.Daemon = issued(t, a, c.Keys.Daemon, c.NodeID, "daemon")
		c.Keys.Broker = issued(t, a, c.Keys.Broker, c.NodeID, "broker")
	}
	hubCfg.ControlPlane = true
	hubCfg.Provisional = true
	hubCfg.LeafPort = freePort(t)
	hubCfg.Leaves = map[string]LeafGrant{pki.Fingerprint(leafCfg.Keys.Broker.Leaf.PublicKey): {NodeID: leafCfg.NodeID, Tasks: []string{"task_assigned"}}}
	leafCfg.FullBrokers = map[string]bool{pki.Fingerprint(hubCfg.Keys.Broker.Leaf.PublicKey): true}
	leafCfg.RemoteLeaf, _ = url.Parse("tls://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(hubCfg.LeafPort)))
	hub, err := Start(hubCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	if _, err := hub.Stores(1); err != nil {
		t.Fatal("genesis stores", err)
	}
	leaf, err := Start(leafCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer leaf.Close()
	deadline := time.Now().Add(5 * time.Second)
	for hub.Server.NumLeafNodes() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hub.Server.NumLeafNodes() != 1 {
		t.Fatal("authenticated leaf did not connect")
	}
	command := "nx.c_leaf.command." + leafCfg.NodeID
	own, _ := leaf.Client.SubscribeSync(command)
	foreign, _ := leaf.Client.SubscribeSync("nx.c_leaf.command.n_someone_else")
	otherLog, _ := hub.Client.SubscribeSync("nx.c_leaf.log.task_unassigned")
	result, _ := hub.Client.SubscribeSync("nx.c_leaf.result." + leafCfg.NodeID)
	leaf.Client.FlushTimeout(time.Second)
	hub.Client.FlushTimeout(time.Second)
	// Interest must propagate across an actual leaf connection before assertions.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		hub.Client.Publish(command, []byte("assigned"))
		if _, err = own.NextMsg(100 * time.Millisecond); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal("assigned delivery unavailable", err)
	}
	hub.Client.Publish("nx.c_leaf.command.n_someone_else", []byte("private"))
	if _, err := foreign.NextMsg(200 * time.Millisecond); err == nil {
		t.Fatal("leaf received another node's command")
	}
	leaf.Client.Publish("nx.c_leaf.log.task_unassigned", []byte("forged"))
	if _, err := otherLog.NextMsg(200 * time.Millisecond); err == nil {
		t.Fatal("leaf published another task's log")
	}
	// Denials must not turn the pending node into an inert/read-only connection.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		leaf.Client.Publish("nx.c_leaf.result."+leafCfg.NodeID, []byte("observed"))
		if _, err = result.NextMsg(100 * time.Millisecond); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal("authorized result delivery unavailable", err)
	}
	if leaf.Server.NumRoutes() != 0 {
		t.Fatal("pending leaf became a route")
	}
	if _, err := leaf.Stores(1); err == nil {
		t.Fatal("pending leaf enabled durable replication")
	}
}
