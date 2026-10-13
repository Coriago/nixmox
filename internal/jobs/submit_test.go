package jobs

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSubmitQueuesInOrderWithLiveLogs(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	r := NewRunner(store)
	entered, release := make(chan struct{}), make(chan struct{})
	r.Register("blocking", func(ctx context.Context, j *Job, out io.Writer) error {
		fmt.Fprint(out, "partial output")
		close(entered)
		<-release
		fmt.Fprint(out, " completed\n")
		return nil
	})
	first, err := r.Submit(Job{Host: "host", Kind: "blocking"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	log, err := os.ReadFile(first.LogPath)
	if err != nil || string(log) != "partial output" {
		t.Fatalf("live partial log: %q %v", log, err)
	}
	second, err := r.Submit(Job{Host: "host", Kind: "echo", Args: []string{"second"}})
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := store.Get(second.ID)
	if pending.State != "pending" {
		t.Fatalf("queued state: %s", pending.State)
	}
	close(release)
	r.Stop()
	a, _ := store.Get(first.ID)
	b, _ := store.Get(second.ID)
	if a.State != "applied" || b.State != "applied" || b.StartedAt.Before(a.UpdatedAt) {
		t.Fatalf("not FIFO: %+v %+v", a, b)
	}
	log, _ = os.ReadFile(first.LogPath)
	if string(log) != "partial output completed\n" {
		t.Fatalf("log bytes changed: %q", log)
	}
}

func TestRecoverPendingAndInterruptedJobs(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	for _, j := range []Job{
		{ID: "waiting", Host: "host", Kind: "echo", State: "pending", Args: []string{"recovered"}},
		{ID: "interrupted", Host: "host", Kind: "echo", State: "building"},
	} {
		if err := store.Create(j); err != nil {
			t.Fatal(err)
		}
	}
	r := NewRunner(store)
	if err := r.Recover(); err != nil {
		t.Fatal(err)
	}
	r.Stop()
	a, _ := store.Get("waiting")
	b, _ := store.Get("interrupted")
	if a.State != "applied" || b.State != "failed" || !strings.Contains(b.Error, "interrupted") {
		t.Fatal(a, b)
	}
}

func TestStoreRejectsTraversal(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	for _, id := range []string{"../escape", "/absolute", "a/b", ""} {
		if err := store.Create(Job{ID: id}); err == nil {
			t.Fatal(id)
		}
		if _, err := store.Get(id); err == nil {
			t.Fatal(id)
		}
		if err := store.AppendLog(id, "x"); err == nil {
			t.Fatal(id)
		}
		if err := store.Update(Job{ID: id}); err == nil {
			t.Fatal(id)
		}
	}
}
