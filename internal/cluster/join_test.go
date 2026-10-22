package cluster

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/machine"
	"nixmox/internal/network"
	"nixmox/internal/pki"
	"nixmox/internal/service"
	"nixmox/internal/storage"
)

func TestJoinTaskRetryRetainsOriginalServerPin(t *testing.T) {
	issuer, _ := issuerFixture(t)
	server := httptest.NewUnstartedServer(machine.New(issuer))
	server.TLS = issuer.TLS()
	server.TLS.Certificates = []tls.Certificate{issuer.keys.Daemon}
	server.StartTLS()
	address := strings.TrimPrefix(server.URL, "https://")
	st, err := storage.Open(filepath.Join(t.TempDir(), "node"))
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
	defer m.Close()
	s, err := service.Open(st, network.Inspector{Config: c}, m.Operations()...)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	payload, _ := json.Marshal(joinPayload{Address: address, ExpectedClusterName: "wrong-name"})
	r, err := s.Admit(domain.Command{ID: "cmd_wrong_name", Type: "cluster.join", TargetID: s.Identity().NodeID, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, s, r.TaskID, "failed")
	task, _ := s.Task(r.TaskID)
	if task.Error == nil || task.Error.Code != "CLUSTER_NAME_MISMATCH" {
		t.Fatal(task.Error)
	}
	server.Close()
	_, other := joiningKeys(t)
	replacement := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("retry sent HTTP to a different server key")
		w.WriteHeader(500)
	}))
	replacement.Listener.Close()
	replacement.Listener, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	replacement.TLS = pki.EnrollmentServer(other.Daemon)
	replacement.StartTLS()
	defer replacement.Close()
	payload, _ = json.Marshal(map[string]string{"task_id": task.ID})
	retry, err := s.Admit(domain.Command{ID: "cmd_retry_join", Type: "task.retry", TargetID: s.Identity().NodeID, Payload: payload, ExpectedVersions: map[string]int64{"task:" + task.ID: task.Version}})
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, s, retry.TaskID, "failed")
	retried, _ := s.Task(retry.TaskID)
	if retried.Error == nil || retried.Error.Code != "IDENTITY_CONFLICT" {
		t.Fatal("retry accepted a new server pin", retried.Error)
	}
}

func TestJoinResumesExactRequestAndConnectsAsPendingLeaf(t *testing.T) {
	issuer, authority := issuerFixture(t)
	server := httptest.NewUnstartedServer(machine.New(issuer))
	server.TLS = issuer.TLS()
	server.TLS.Certificates = []tls.Certificate{issuer.keys.Daemon}
	server.StartTLS()
	defer server.Close()
	st, err := storage.Open(filepath.Join(t.TempDir(), "joining"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := config.Default()
	c.DataDir = st.Root
	c.NATS.ClientPort = -1
	c.NATS.RoutePort = -1
	c.NATS.LeafPort = -1
	var m *Manager
	var s *service.Service
	open := func() {
		m, err = Open(st, c)
		if err != nil {
			t.Fatal(err)
		}
		s, err = service.Open(st, network.Inspector{Config: c}, m.Operations()...)
		if err != nil {
			m.Close()
			t.Fatal(err)
		}
		if err = m.Attach(s); err != nil {
			t.Fatal(err)
		}
	}
	open()
	defer func() { s.Close(); m.Close() }()
	original := s.Identity()
	daemonKey := pki.Fingerprint(m.keys.Daemon.Leaf.PublicKey)
	brokerKey := pki.Fingerprint(m.keys.Broker.Leaf.PublicKey)
	payload, _ := json.Marshal(joinPayload{Address: strings.TrimPrefix(server.URL, "https://"), ExpectedClusterName: "homelab"})
	cmd := domain.Command{ID: "cmd_join", Type: "cluster.join", TargetID: original.NodeID, Payload: payload}
	r, err := s.Admit(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var before joinCheckpoint
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := s.Task(r.TaskID)
		json.Unmarshal(task.Checkpoint, &before)
		if before.Request != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if before.Request == nil {
		t.Fatal("request was not persisted")
	}
	if s.Identity().ClusterID != nil || issuer.broker.Server.NumLeafNodes() != 0 {
		t.Fatal("unapproved node gained membership")
	}
	s.Close()
	m.Close()
	open()
	replay, err := s.Admit(cmd)
	if err != nil || replay != r {
		t.Fatal("join retry lost receipt", err)
	}
	request, err := issuer.JoinRequest(before.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision, _ := json.Marshal(decisionPayload{RequestID: request.ID})
	approved, err := authority.Admit(domain.Command{ID: "cmd_approve_join", Type: "cluster.request.approve", TargetID: authority.Identity().NodeID, Payload: decision, ExpectedVersions: map[string]int64{"join_request:" + request.ID: request.Version}})
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, authority, approved.TaskID, "succeeded")
	waitTask(t, s, r.TaskID, "succeeded")
	joined := s.Identity()
	if joined.ClusterID == nil || *joined.ClusterID != *authority.Identity().ClusterID || joined.NodeID != original.NodeID || joined.KeyID != original.KeyID || joined.MembershipEpoch != 1 {
		t.Fatal("identity continuity lost", joined)
	}
	if pki.Fingerprint(m.keys.Daemon.Leaf.PublicKey) != daemonKey || pki.Fingerprint(m.keys.Broker.Leaf.PublicKey) != brokerKey {
		t.Fatal("enrollment replaced private keys")
	}
	deadline = time.Now().Add(5 * time.Second)
	for issuer.broker.Server.NumLeafNodes() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if issuer.broker.Server.NumLeafNodes() != 1 || m.broker.Server.NumRoutes() != 0 || m.stores != nil {
		t.Fatal("pending leaf topology is incorrect")
	}
	status, err := m.Status()
	if err != nil || status.Membership != "pending" || status.BrokerRole != "leaf" || !status.Quorum.Available || status.FullMembers != 0 {
		t.Fatal("pending status query", status, err)
	}
	if _, err = m.broker.Stores(1); err == nil {
		t.Fatal("pending leaf gained JetStream")
	}
	var cp joinCheckpoint
	task, _ := s.Task(r.TaskID)
	if json.Unmarshal(task.Checkpoint, &cp) != nil || cp.Request.ID != before.Request.ID || cp.ServerCertificate != before.ServerCertificate {
		t.Fatal("restart changed enrollment attempt")
	}
	if _, err = os.Stat(filepath.Join(st.Root, "pki/issuer/key.pem")); err == nil {
		t.Fatal("issuer private key transferred")
	}
	s.Close()
	m.Close()
	open()
	if s.Identity().ClusterID == nil || m.membership.JoinRequestID != before.Request.ID || m.stores != nil {
		t.Fatal("pending membership lost on restart")
	}
	replay, err = s.Admit(cmd)
	if err != nil || replay != r {
		t.Fatal("enrolled restart lost receipt", err)
	}
}
