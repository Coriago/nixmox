package cluster

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/machine"
	"nixmox/internal/network"
	"nixmox/internal/service"
)

func joinFixture(t *testing.T, issuer *Manager, authority *service.Service, m *Manager, s *service.Service) {
	t.Helper()
	server := httptest.NewUnstartedServer(machine.New(issuer))
	server.TLS = issuer.TLS()
	server.TLS.Certificates = []tls.Certificate{issuer.keys.Daemon}
	server.StartTLS()
	t.Cleanup(server.Close)
	payload, _ := json.Marshal(joinPayload{Address: strings.TrimPrefix(server.URL, "https://"), ExpectedClusterName: "homelab"})
	r, err := s.Admit(domain.Command{ID: "cmd_join", Type: "cluster.join", TargetID: s.Identity().NodeID, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var request domain.JoinRequest
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		page, err := issuer.Requests("awaiting_approval", "100", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range page.Items {
			if v.NodeID == s.Identity().NodeID {
				request = v
			}
		}
		if request.ID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if request.ID == "" {
		t.Fatal("missing join request")
	}
	payload, _ = json.Marshal(decisionPayload{RequestID: request.ID})
	approved, err := authority.Admit(domain.Command{ID: "cmd_approve_" + request.ID, Type: "cluster.request.approve", TargetID: authority.Identity().NodeID, Payload: payload, ExpectedVersions: map[string]int64{"join_request:" + request.ID: request.Version}})
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, authority, approved.TaskID, "succeeded")
	waitTask(t, s, r.TaskID, "succeeded")
}

func TestDurableRemoteResultSurvivesCoordinatorRestart(t *testing.T) {
	var runs, resolves atomic.Int32
	op := service.Operation{Kind: "host.refresh", Scope: "cluster", Timeout: 5 * time.Second, SafeRetry: true,
		Validate: func(c domain.Command) error {
			var p struct {
				HostID string `json:"host_id"`
			}
			return domain.StrictJSON(c.Payload, &p)
		},
		Resolve: func(context.Context, domain.Command) (json.RawMessage, error) {
			resolves.Add(1)
			return json.RawMessage(`{"pin":"original"}`), nil
		},
		Run: func(ctx context.Context, e *service.Execution) (any, error) { runs.Add(1); return "observed", nil }}
	issuer, authority := issuerFixture(t, op)
	leaf, target := nodeFixture(t, op)
	joinFixture(t, issuer, authority, leaf, target)
	if err := issuer.hub.results.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"host_id": target.Identity().NodeID})
	command := domain.Command{ID: "cmd_lost_result", Type: "host.refresh", TargetID: target.Identity().NodeID, Payload: payload}
	_, err := authority.Admit(command)
	if err == nil {
		t.Fatal("receipt completed while result consumer was disconnected")
	}
	task := waitTask(t, target, domain.CommandTaskID(command.ID), "succeeded")
	status, err := issuer.broker.Server.Varz(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := issuer.config
	cfg.NATS.LeafPort = status.LeafNode.Port
	authority.Close()
	issuer.Close()
	restarted, err := Open(issuer.store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := service.Open(issuer.store, network.Inspector{Config: cfg}, append(restarted.Operations(), op)...)
	if err != nil {
		restarted.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); restarted.Close() })
	if err = restarted.Attach(s); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var recovered domain.Receipt
	for time.Now().Before(deadline) {
		recovered, err = s.Receipt(command.ID)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || recovered.TaskID != task.ID || runs.Load() != 1 || resolves.Load() != 1 {
		t.Fatal("lost result repeated execution or resolution", recovered, err, runs.Load(), resolves.Load())
	}
	remote := waitTask(t, s, task.ID, "succeeded")
	if string(remote.Result) != string(task.Result) {
		t.Fatal("recovered projection lost owner result")
	}
	if replay, err := s.Admit(command); err != nil || replay.TaskID != task.ID {
		t.Fatal("coordinator restart lost receipt", replay, err)
	}
}

func TestPendingDurableExecutionAndRemoteTaskProjection(t *testing.T) {
	var resolves, runs atomic.Int32
	effect := filepath.Join(t.TempDir(), "effect")
	op := service.Operation{Kind: "host.refresh", Scope: "cluster", Timeout: 5 * time.Second, SafeRetry: true,
		Validate: func(c domain.Command) error {
			var p struct {
				HostID string `json:"host_id"`
			}
			return domain.StrictJSON(c.Payload, &p)
		},
		Resolve: func(context.Context, domain.Command) (json.RawMessage, error) {
			resolves.Add(1)
			return json.RawMessage(`{"source":"pinned"}`), nil
		},
		Run: func(ctx context.Context, e *service.Execution) (any, error) {
			if string(e.Resolved) != `{"source":"pinned"}` {
				t.Error("resolved identity lost")
			}
			runs.Add(1)
			if err := e.Checkpoint("observing", struct{}{}, true); err != nil {
				return nil, err
			}
			if e.Command.ID == "cmd_remote_cancel" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if err := e.Log([]byte(strings.Repeat("progress\n", 7000))); err != nil {
				return nil, err
			}
			if err := os.WriteFile(effect, []byte(e.Command.TargetID), 0600); err != nil {
				return nil, err
			}
			return map[string]string{"observed_on": e.Command.TargetID}, nil
		}}
	issuer, authority := issuerFixture(t, op)
	leaf, target := nodeFixture(t, op)
	joinFixture(t, issuer, authority, leaf, target)
	node := target.Identity().NodeID
	cluster := *target.Identity().ClusterID
	payload, _ := json.Marshal(map[string]string{"host_id": node})
	command := domain.Command{ID: "cmd_remote", Type: "host.refresh", TargetID: node, Payload: payload}
	// A Core publication has no authoritative admission record and must never
	// reach execution, even if the stream happens to capture that publication.
	forged := domain.PreparedCommand{Command: command, ClusterID: cluster, CoordinatorID: authority.Identity().NodeID, TargetEpoch: 1, Kind: command.Type, Resolved: json.RawMessage(`{}`), ExpiresAt: time.Now().Add(time.Minute)}
	forged.Command.ID = "cmd_unadmitted"
	data, _ := json.Marshal(forged)
	issuer.broker.Client.Publish("nx."+cluster+".command."+node, data)
	time.Sleep(300 * time.Millisecond)
	if runs.Load() != 0 {
		t.Fatal("unadmitted publication executed")
	}
	receipt, err := authority.Admit(command)
	if err != nil {
		t.Fatal(err)
	}
	local := waitTask(t, target, receipt.TaskID, "succeeded")
	remote := waitTask(t, authority, receipt.TaskID, "succeeded")
	if local.OwnerNodeID != node || remote.OwnerNodeID != node || remote.CoordinatorID != authority.Identity().NodeID || remote.MembershipEpoch != 1 || string(local.Result) != string(remote.Result) {
		t.Fatal("remote task binding lost", remote)
	}
	if runs.Load() != 1 || resolves.Load() != 1 {
		t.Fatal("duplicate resolution or execution", runs.Load(), resolves.Load())
	}
	content, err := os.ReadFile(effect)
	if err != nil || string(content) != node {
		t.Fatal("effect did not run on target", err)
	}
	if again, err := authority.Admit(command); err != nil || again.TaskID != receipt.TaskID {
		t.Fatal("remote receipt retry changed", again, err)
	}
	page, err := authority.ListTasks(service.TaskFilter{HostID: node}, "100", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != receipt.TaskID {
		t.Fatal("remote task missing from list", page, err)
	}
	if leaf.stores != nil || leaf.broker.Server.NumRoutes() != 0 {
		t.Fatal("execution granted voting membership")
	}
	chunk, err := authority.Logs(remote.ID, 0, 65536)
	if err != nil {
		t.Fatal("remote log chunk", err)
	}
	bytes, err := chunk.Bytes()
	if err != nil || len(bytes) != queryChunk || chunk.NextOffset != queryChunk {
		t.Fatal("remote query chunk was not bounded", chunk.NextOffset, err)
	}
	log, complete, err := authority.LogSnapshot(remote.ID)
	if err != nil || !complete || string(log.Data) != strings.Repeat("progress\n", 7000) {
		t.Fatal("remote fixed log snapshot", complete, err)
	}
	if _, err = authority.Logs(remote.ID, log.End()+1, 1024); err == nil {
		t.Fatal("remote out-of-range log read succeeded")
	}
	localReceipt, err := target.Admit(domain.Command{ID: "cmd_private_local", Type: "network.check", TargetID: node, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var denied logBounds
	if err = issuer.queryOwner(context.Background(), node, ownerRequest{Kind: "task.log_bounds", TaskID: localReceipt.TaskID}, &denied); err == nil {
		t.Fatal("leaf exposed a task outside its cluster assignment scope")
	}
	payload, _ = json.Marshal(map[string]string{"task_id": remote.ID})
	retry, err := authority.Admit(domain.Command{ID: "cmd_remote_retry", Type: "task.retry", TargetID: node, Payload: payload, ExpectedVersions: map[string]int64{"task:" + remote.ID: remote.Version}})
	if err != nil {
		t.Fatal("remote retry admission", err)
	}
	retried := waitTask(t, authority, retry.TaskID, "succeeded")
	if retried.ParentID == nil || *retried.ParentID != remote.ID || retried.ID == remote.ID || runs.Load() != 2 || resolves.Load() != 1 {
		t.Fatal("retry changed original pin or task lineage", retried, resolves.Load(), runs.Load())
	}
	command.ID = "cmd_remote_cancel"
	active, err := authority.Admit(command)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var review domain.Task
	for time.Now().Before(deadline) {
		review, _ = authority.Task(active.TaskID)
		if review.State == "running" && review.Phase == "observing" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if review.Phase != "observing" {
		t.Fatal("remote cancellation review missing")
	}
	payload, _ = json.Marshal(map[string]string{"task_id": review.ID})
	cancelled, err := target.Admit(domain.Command{ID: "cmd_cancel_remote", Type: "task.cancel", TargetID: node, Payload: payload, ExpectedVersions: map[string]int64{"task:" + review.ID: review.Version}})
	if err != nil || cancelled.TaskID != review.ID {
		t.Fatal("remote cancel admission", cancelled, err)
	}
	waitTask(t, target, review.ID, "cancelled")
	waitTask(t, authority, review.ID, "cancelled")
	if receipt, err := target.Receipt("cmd_cancel_remote"); err != nil || receipt.TaskID != review.ID {
		t.Fatal("pending gateway cannot read its durable receipt", receipt, err)
	}
	if _, err := target.Receipt("cmd_genesis"); err == nil {
		t.Fatal("pending gateway read another executor's command")
	}
	command.ID = "cmd_pending_refresh"
	pending, err := target.Admit(command)
	if err != nil {
		t.Fatal("pending gateway did not use durable admission", err)
	}
	waitTask(t, target, pending.TaskID, "succeeded")
	waitTask(t, authority, pending.TaskID, "succeeded")
}
