package jobs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestRecoverNeverReplaysUSBWrite(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.Create(Job{ID: "usb", Kind: "installer-write", Host: "local", State: "pending"}); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store)
	if err := runner.Recover(); err != nil {
		t.Fatal(err)
	}
	runner.Stop()
	j, _ := store.Get("usb")
	if j.State != "failed" || !strings.Contains(j.Error, "fresh confirmation") {
		t.Fatalf("%+v", j)
	}
}

func TestLogAppendOffsetsDescribePersistedBytes(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	appender := store.(interface {
		AppendLogChunk(string, string) (LogChunk, error)
	})
	first, err := appender.AppendLogChunk("job-log", "café\n")
	if err != nil || first.Offset != 0 || first.Next != int64(len("café\n")) {
		t.Fatal(first, err)
	}
	next, err := appender.AppendLogChunk("job-log", "next\n")
	if err != nil || next.Offset != first.Next || next.Next != first.Next+5 {
		t.Fatal(next, err)
	}
}

func TestRecoverRequiresNewConfirmationForOnboarding(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store)
	for _, kind := range []string{"host-onboard", "host-install"} {
		if err := store.Create(Job{ID: kind, Kind: kind, Host: "installer", State: "pending"}); err != nil {
			t.Fatal(err)
		}
		runner.Register(kind, func(context.Context, *Job, io.Writer) error {
			t.Error("resumed destructive task without confirmation")
			return nil
		})
	}
	if err := runner.Recover(); err != nil {
		t.Fatal(err)
	}
	runner.Stop()
	for _, kind := range []string{"host-onboard", "host-install"} {
		j, err := store.Get(kind)
		if err != nil || j.State != "failed" {
			t.Fatal(j, err)
		}
	}
}
