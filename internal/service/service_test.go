package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/storage"
)

type inspectFunc func(context.Context, string) (domain.NetworkStatus, error)

func (f inspectFunc) Inspect(c context.Context, n string) (domain.NetworkStatus, error) {
	return f(c, n)
}
func good(ctx context.Context, node string) (domain.NetworkStatus, error) {
	n := domain.UnknownNetwork(node)
	now := time.Now().UTC()
	n.ObservedAt = &now
	n.Network.CheckedAt = &now
	n.Freshness = "live"
	n.Network.Status = "disconnected"
	return n, nil
}
func blocked(ctx context.Context, node string) (domain.NetworkStatus, error) {
	<-ctx.Done()
	return domain.NetworkStatus{}, ctx.Err()
}
func setup(t *testing.T, f inspectFunc) (*Service, *storage.Store) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st, f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); st.Close() })
	return s, st
}
func command(s *Service, id string) domain.Command {
	return domain.Command{ID: id, Type: "network.check", TargetID: s.Identity().NodeID, Payload: json.RawMessage(`{}`)}
}
func await(t *testing.T, s *Service, id, state string) domain.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v, err := s.Task(id)
		if err == nil && v.State == state {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	v, err := s.Task(id)
	t.Fatalf("waiting for %s: %+v, %v", state, v, err)
	return v
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *domain.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("wanted %s, got %v", want, err)
	}
}
func TestConcurrentIdempotencyAndRestart(t *testing.T) {
	var calls atomic.Int32
	s, st := setup(t, func(c context.Context, n string) (domain.NetworkStatus, error) { calls.Add(1); return good(c, n) })
	cmd := command(s, "cmd_same")
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Admit(cmd)
			if err != nil || r.TaskID != taskID(cmd.ID) {
				t.Errorf("%+v %v", r, err)
			}
		}()
	}
	wg.Wait()
	await(t, s, taskID(cmd.ID), "succeeded")
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	changed := cmd
	changed.Payload = json.RawMessage(`{"password":"not-for-storage"}`)
	_, err := s.Admit(changed)
	code(t, err, "IDEMPOTENCY_CONFLICT")
	s.Close()
	reopened, err := Open(st, inspectFunc(good))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	r, err := reopened.Admit(cmd)
	if err != nil || r.TaskID != taskID(cmd.ID) || len(reopened.Tasks()) != 1 {
		t.Fatal(r, err)
	}
}
func TestAdmissionCrashBoundaries(t *testing.T) {
	for _, boundary := range []string{"intent", "task", "receipt"} {
		t.Run(boundary, func(t *testing.T) {
			s, st := setup(t, inspectFunc(good))
			s.mu.Lock()
			s.checkpoint = func(name string) error {
				if name == boundary {
					return errors.New("simulated loss")
				}
				return nil
			}
			s.mu.Unlock()
			cmd := command(s, "cmd_crash")
			_, err := s.Admit(cmd)
			code(t, err, "OUTCOME_UNKNOWN")
			s.Close()
			reopened, err := Open(st, inspectFunc(good))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			r, err := reopened.Admit(cmd)
			if err != nil {
				t.Fatal(err)
			}
			await(t, reopened, r.TaskID, "succeeded")
			if len(reopened.Tasks()) != 1 {
				t.Fatal("duplicate task")
			}
		})
	}
}
func TestCancelVersionRetryAndRecovery(t *testing.T) {
	s, st := setup(t, inspectFunc(blocked))
	r, err := s.Admit(command(s, "cmd_block"))
	if err != nil {
		t.Fatal(err)
	}
	task := await(t, s, r.TaskID, "running")
	cmd := domain.Command{ID: "cmd_cancel", Type: "task.cancel", TargetID: s.Identity().NodeID, Payload: json.RawMessage(`{"task_id":"` + task.ID + `"}`), ExpectedVersions: map[string]int64{"task:" + task.ID: task.Version - 1}}
	_, err = s.Admit(cmd)
	code(t, err, "VERSION_CONFLICT")
	cmd.ExpectedVersions["task:"+task.ID] = task.Version
	s.mu.Lock()
	s.checkpoint = func(name string) error {
		if name == "task" {
			return errors.New("interrupted cancellation")
		}
		return nil
	}
	s.mu.Unlock()
	_, err = s.Admit(cmd)
	code(t, err, "OUTCOME_UNKNOWN")
	s.Close()
	reopened, err := Open(st, inspectFunc(good))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cancelled := await(t, reopened, task.ID, "cancelled")
	if !cancelled.Cancellation.Requested {
		t.Fatal("lost cancellation")
	}
	cmd.ID = "cmd_retry"
	cmd.Type = "task.retry"
	cmd.ExpectedVersions["task:"+task.ID] = cancelled.Version
	retry, err := reopened.Admit(cmd)
	if err != nil {
		t.Fatal(err)
	}
	result := await(t, reopened, retry.TaskID, "succeeded")
	if result.ParentID == nil || *result.ParentID != task.ID {
		t.Fatal("missing retry lineage")
	}
	old, _ := reopened.Task(task.ID)
	if old.State != "cancelled" || old.Version != cancelled.Version {
		t.Fatal("terminal task changed")
	}
}
func TestRunningObservationSafelyReconciles(t *testing.T) {
	s, st := setup(t, inspectFunc(blocked))
	r, err := s.Admit(command(s, "cmd_running"))
	if err != nil {
		t.Fatal(err)
	}
	before := await(t, s, r.TaskID, "running")
	s.Close()
	reopened, err := Open(st, inspectFunc(good))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after := await(t, reopened, r.TaskID, "succeeded")
	if after.Version <= before.Version+1 {
		t.Fatal("reconciliation was not recorded")
	}
}
func TestAdmissionBoundsAndSecrets(t *testing.T) {
	s, st := setup(t, inspectFunc(blocked))
	cmd := command(s, "cmd_secret")
	cmd.Payload = json.RawMessage(`{"password":"secret-sentinel"}`)
	_, err := s.Admit(cmd)
	code(t, err, "INVALID_PAYLOAD")
	cmd = command(s, "cmd_cluster")
	cmd.Type = "deployment.apply"
	_, err = s.Admit(cmd)
	code(t, err, "QUORUM_UNAVAILABLE")
	for range domain.QueueCapacity {
		_, err := s.Admit(command(s, domain.NewID("cmd_")))
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Admit(command(s, "cmd_overflow"))
	code(t, err, "QUEUE_FULL")
	names, err := st.Names("receipts")
	if err != nil || len(names) != domain.QueueCapacity {
		t.Fatal(names, err)
	}
	if _, err = s.Receipt("cmd_secret"); err == nil {
		t.Fatal("secret was persisted")
	}
}
func TestPaginationAndStaleness(t *testing.T) {
	s, _ := setup(t, inspectFunc(good))
	for range 3 {
		r, err := s.Admit(command(s, domain.NewID("cmd_")))
		if err != nil {
			t.Fatal(err)
		}
		await(t, s, r.TaskID, "succeeded")
	}
	p, err := s.ListTasks(TaskFilter{}, "2", "")
	if err != nil || len(p.Items) != 2 || p.NextCursor == nil {
		t.Fatal(p, err)
	}
	next, err := s.ListTasks(TaskFilter{}, "2", *p.NextCursor)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == p.Items[0].ID {
		t.Fatal(next, err)
	}
	_, err = s.ListTasks(TaskFilter{State: "succeeded"}, "2", *p.NextCursor)
	code(t, err, "INVALID_QUERY")
	_, err = s.ListTasks(TaskFilter{}, "501", "")
	code(t, err, "INVALID_QUERY")
	s.mu.Lock()
	past := time.Now().Add(-time.Minute)
	s.network.ObservedAt = &past
	s.mu.Unlock()
	if s.Network().Freshness != "stale" {
		t.Fatal("old data is live")
	}
}

// A real process exits without defers after each admission fsync boundary. This
// verifies recovery from on-disk evidence, not just an in-memory mocked store.
func TestProcessCrashRecovery(t *testing.T) {
	if dir := os.Getenv("NIXMOX_CRASH_TEST_DIR"); dir != "" {
		st, err := storage.Open(dir)
		if err != nil {
			os.Exit(90)
		}
		s, err := Open(st, inspectFunc(good))
		if err != nil {
			os.Exit(91)
		}
		boundary := os.Getenv("NIXMOX_CRASH_TEST_BOUNDARY")
		s.mu.Lock()
		s.checkpoint = func(at string) error {
			if at == boundary {
				_ = syscallKillSelf()
			}
			return nil
		}
		s.mu.Unlock()
		_, _ = s.Admit(command(s, "cmd_process_crash"))
		os.Exit(92)
	}
	for _, boundary := range []string{"intent", "task", "receipt"} {
		t.Run(boundary, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessCrashRecovery$")
			cmd.Env = append(os.Environ(), "NIXMOX_CRASH_TEST_DIR="+dir, "NIXMOX_CRASH_TEST_BOUNDARY="+boundary)
			if err := cmd.Run(); err == nil {
				t.Fatal("child did not crash")
			}
			st, err := storage.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			s, err := Open(st, inspectFunc(good))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			receipt, err := s.Receipt("cmd_process_crash")
			if err != nil {
				t.Fatal(err)
			}
			await(t, s, receipt.TaskID, "succeeded")
			retry, err := s.Admit(command(s, "cmd_process_crash"))
			if err != nil || retry.TaskID != receipt.TaskID || len(s.Tasks()) != 1 {
				t.Fatal(retry, err)
			}
		})
	}
}
