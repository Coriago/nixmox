package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/network"
	"nixmox/internal/service"
	"nixmox/internal/storage"
)

func waitTask(t *testing.T, s *service.Service, id, state string) domain.Task {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, err := s.Task(id)
		if err == nil && task.State == state {
			return task
		}
		time.Sleep(5 * time.Millisecond)
	}
	task, err := s.Task(id)
	t.Fatalf("waiting for %s: %+v %v", state, task, err)
	return task
}
func TestDurableClusterAdmissionRecoveryAndLocalAvailability(t *testing.T) {
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
	var resolved, runs, reconciled atomic.Int32
	effect := filepath.Join(t.TempDir(), "effect")
	op := service.Operation{Kind: "host.refresh", Scope: "cluster", Timeout: 30 * time.Second, SafeRetry: true, Validate: func(c domain.Command) error {
		var p struct {
			HostID string `json:"host_id"`
		}
		return domain.StrictJSON(c.Payload, &p)
	}, Resolve: func(context.Context, domain.Command) (json.RawMessage, error) {
		resolved.Add(1)
		return json.RawMessage(`{"commit":"pinned"}`), nil
	}, Run: func(ctx context.Context, e *service.Execution) (any, error) {
		runs.Add(1)
		if err := e.Checkpoint("effect-intent", map[string]string{"path": effect}, true); err != nil {
			return nil, err
		}
		if err := os.WriteFile(effect, []byte("observed"), 0600); err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}, Reconcile: func(ctx context.Context, e *service.Execution) (any, error) {
		reconciled.Add(1)
		if string(e.Resolved) != `{"commit":"pinned"}` {
			t.Errorf("lost pin %s", e.Resolved)
		}
		b, err := os.ReadFile(effect)
		return string(b), err
	}}
	open := func() (*Manager, *service.Service) {
		m, err := Open(st, c)
		if err != nil {
			t.Fatal(err)
		}
		s, err := service.Open(st, network.Inspector{Config: c}, append(m.Operations(), op)...)
		if err != nil {
			m.Close()
			t.Fatal(err)
		}
		if err = m.Attach(s); err != nil {
			s.Close()
			m.Close()
			t.Fatal(err)
		}
		return m, s
	}
	m, s := open()
	genesis, err := s.Admit(domain.Command{ID: "cmd_genesis", Type: "cluster.create", TargetID: s.Identity().NodeID, Payload: json.RawMessage(`{"name":"test"}`)})
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, s, genesis.TaskID, "succeeded")
	payload, _ := json.Marshal(map[string]string{"host_id": s.Identity().NodeID})
	command := domain.Command{ID: "cmd_durable", Type: "host.refresh", TargetID: s.Identity().NodeID, Payload: payload}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Admit(command); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	receipt, err := s.Receipt(command.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(effect); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if resolved.Load() != 1 || runs.Load() != 1 {
		t.Fatalf("resolved %d, ran %d", resolved.Load(), runs.Load())
	}
	s.Close()
	m.Close()
	m, s = open()
	defer m.Close()
	defer s.Close()
	task := waitTask(t, s, receipt.TaskID, "succeeded")
	if task.Scope != "cluster" || task.ClusterID == nil || runs.Load() != 1 || reconciled.Load() != 1 || resolved.Load() != 1 {
		t.Fatal("incorrect recovery", task, runs.Load(), reconciled.Load(), resolved.Load())
	}
	retry, err := s.Admit(command)
	if err != nil || retry.TaskID != receipt.TaskID {
		t.Fatal(retry, err)
	}
	changed := command
	changed.ExpectedVersions = map[string]int64{"host:" + command.TargetID: 1}
	_, err = s.Admit(changed)
	var conflict *domain.Error
	if !errors.As(err, &conflict) || conflict.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatal("request identity lost", err)
	}
	m.broker.Server.Shutdown()
	m.broker.Server.WaitForShutdown()
	command.ID = "cmd_without_quorum"
	if _, err = s.Admit(command); err == nil {
		t.Fatal("cluster command accepted without quorum")
	}
	local, err := s.Admit(domain.Command{ID: "cmd_local", Type: "network.check", TargetID: s.Identity().NodeID, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal("local scope depended on cluster", err)
	}
	waitTask(t, s, local.TaskID, "succeeded")
	control, _ := json.Marshal(map[string]string{"task_id": task.ID})
	_, err = s.Admit(domain.Command{ID: "cmd_cluster_retry", Type: "task.retry", TargetID: s.Identity().NodeID, ExpectedVersions: map[string]int64{"task:" + task.ID: task.Version}, Payload: control})
	if err == nil {
		t.Fatal("cluster retry fell back to local admission")
	}
}
