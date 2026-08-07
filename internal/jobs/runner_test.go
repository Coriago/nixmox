package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunner(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	runner := NewRunner(store)

	t.Run("echo job", func(t *testing.T) {
		j := Job{ID: "job-echo", Kind: "echo", Host: "host1", Args: []string{"hello"}}
		store.Create(j)

		terminalFired := false
		runner.OnTerminal = func(job Job) {
			if job.ID == j.ID {
				terminalFired = true
				if job.State != "applied" {
					t.Errorf("OnTerminal state = %s, want applied", job.State)
				}
			}
		}

		if err := runner.Start(context.Background(), j); err != nil {
			t.Fatalf("Start: %v", err)
		}

		runner.Stop()

		got, _ := store.Get(j.ID)
		if got.State != "applied" {
			t.Errorf("State = %s, want applied", got.State)
		}

		logData, _ := os.ReadFile(filepath.Join(dir, "jobs", j.ID+".log"))
		if !strings.Contains(string(logData), "hello") {
			t.Errorf("Log does not contain hello: %q", string(logData))
		}
		if !terminalFired {
			t.Error("OnTerminal not fired")
		}
	})

	t.Run("failed job", func(t *testing.T) {
		j := Job{ID: "job-fail", Kind: "echo", Host: "host2", Args: []string{"-n", "hello"}}
		j.Kind = "unknown"
		store.Create(j)

		terminalFired := false
		runner.OnTerminal = func(job Job) {
			if job.ID == j.ID {
				terminalFired = true
				if job.State != "failed" {
					t.Errorf("OnTerminal state = %s, want failed", job.State)
				}
			}
		}

		if err := runner.Start(context.Background(), j); err != nil {
			t.Fatalf("Start: %v", err)
		}

		runner.Stop()

		got, _ := store.Get(j.ID)
		if got.State != "failed" {
			t.Errorf("State = %s, want failed", got.State)
		}
		if !terminalFired {
			t.Error("OnTerminal not fired")
		}
	})

	t.Run("host busy", func(t *testing.T) {
		j1 := Job{ID: "job-busy-1", Kind: "echo", Host: "host3", Args: []string{"hello"}}
		j2 := Job{ID: "job-busy-2", Kind: "echo", Host: "host3", Args: []string{"world"}}
		store.Create(j1)
		store.Create(j2)

		v, _ := runner.mu.LoadOrStore("host3", &sync.Mutex{})
		hostMu := v.(*sync.Mutex)
		hostMu.Lock()

		err := runner.Start(context.Background(), j2)
		if err != ErrHostBusy {
			t.Errorf("Start = %v, want ErrHostBusy", err)
		}

		hostMu.Unlock()
	})

	t.Run("context canceled", func(t *testing.T) {
		j := Job{ID: "job-cancel", Kind: "echo", Host: "host4", Args: []string{"hello"}}
		store.Create(j)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := runner.Start(ctx, j); err != nil {
			t.Fatalf("Start: %v", err)
		}

		runner.Stop()

		got, _ := store.Get(j.ID)
		if got.State != "failed" {
			t.Errorf("State = %s, want failed", got.State)
		}

		logData, _ := os.ReadFile(filepath.Join(dir, "jobs", j.ID+".log"))
		if !strings.Contains(string(logData), "context canceled") {
			t.Errorf("Log does not contain context canceled: %q", string(logData))
		}
	})
}
