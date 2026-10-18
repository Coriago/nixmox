package jobs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCancelQueuedAndRunning(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	r := NewRunner(store)
	t.Cleanup(r.Shutdown)
	entered, released := make(chan struct{}), make(chan struct{})
	r.Register("blocking", func(ctx context.Context, j *Job, out io.Writer) error {
		close(entered)
		<-ctx.Done()
		// A late progress update must not erase cancellation.
		j.Phase = "late progress"
		if err := store.Update(*j); err != nil {
			return err
		}
		close(released)
		return ctx.Err()
	})
	first, _ := r.Submit(Job{Host: "host", Kind: "blocking"})
	<-entered
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	queued, _ := r.Submit(Job{Host: "host", Kind: "command", Command: "sh", Args: []string{"-c", "touch \"$1\"", "sh", marker}})
	if j, err := r.Cancel(queued.ID); err != nil || j.State != "cancelled" || !j.StartedAt.IsZero() {
		t.Fatalf("queued cancellation: %+v %v", j, err)
	}
	if _, err := r.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("running action did not stop")
	}
	next, _ := r.Submit(Job{Host: "host", Kind: "echo", Args: []string{"queue still works"}})
	r.Stop()
	for _, id := range []string{first.ID, queued.ID} {
		j, _ := store.Get(id)
		if j.State != "cancelled" {
			t.Fatalf("%s: %+v", id, j)
		}
		if _, err := r.Cancel(id); err != nil {
			t.Fatal("repeat cancel", err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cancelled queued command executed")
	}
	if j, _ := store.Get(next.ID); j.State != "applied" {
		t.Fatal(j)
	}
	if _, err := r.Cancel(next.ID); !errors.Is(err, ErrNotCancellable) {
		t.Fatal(err)
	}
	recovered := NewRunner(store)
	defer recovered.Shutdown()
	if err := recovered.Recover(); err != nil {
		t.Fatal(err)
	}
	recovered.Stop()
	if j, _ := store.Get(queued.ID); j.State != "cancelled" {
		t.Fatal(j)
	}
}

func TestCancelKillsCommandChildren(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	r := NewRunner(store)
	defer r.Shutdown()
	marker := filepath.Join(t.TempDir(), "child-survived")
	j, err := r.Submit(Job{Host: "host", Kind: "command", Command: "sh", Args: []string{"-c", "(sleep 1; touch \"$1\") & echo ready; wait", "sh", marker}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(j.LogPath)
		if strings.Contains(string(b), "\nready\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := r.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	r.Stop()
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command child survived cancellation")
	}
	got, _ := store.Get(j.ID)
	if got.State != "cancelled" {
		t.Fatal(got)
	}
}

func TestCancellationCannotBeOverwrittenBySuccess(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	r := NewRunner(store)
	defer r.Shutdown()
	entered, release := make(chan struct{}), make(chan struct{})
	r.Register("late-success", func(ctx context.Context, j *Job, out io.Writer) error {
		close(entered)
		<-release
		if err := store.Update(*j); err != nil {
			return err
		}
		if got, _ := store.Get(j.ID); got.State != "cancelling" {
			t.Errorf("late progress erased cancellation: %s", got.State)
		}
		return nil
	})
	j, _ := r.Submit(Job{Host: "host", Kind: "late-success"})
	<-entered
	_, err := r.Cancel(j.ID)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	r.Stop()
	if got, _ := store.Get(j.ID); got.State != "cancelled" {
		t.Fatal(got)
	}
}

func TestRecoverCancellationRequest(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.Create(Job{ID: "interrupted-cancellation", Host: "host", Kind: "echo", State: "cancelling"}); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(store)
	defer r.Shutdown()
	if err := r.Recover(); err != nil {
		t.Fatal(err)
	}
	r.Stop()
	got, _ := store.Get("interrupted-cancellation")
	if got.State != "cancelled" || !strings.Contains(got.Error, "restart") {
		t.Fatal(got)
	}
}
