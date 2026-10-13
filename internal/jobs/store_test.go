package jobs

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	now := time.Now().Truncate(time.Millisecond)

	j1 := Job{
		ID:        "job-1",
		Kind:      "echo",
		Host:      "host1",
		Args:      []string{"hello"},
		State:     "pending",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.Create(j1); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.Create(j1); err == nil {
		t.Fatal("Create duplicate should fail")
	}

	got, err := store.Get("job-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != j1.ID || got.State != j1.State {
		t.Errorf("Get mismatch: got %+v, want %+v", got, j1)
	}

	got.State = "building"
	got.UpdatedAt = time.Now().Truncate(time.Millisecond)
	if err := store.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got2, err := store.Get("job-1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got2.State != "building" {
		t.Errorf("Update failed: got state %q, want building", got2.State)
	}

	j2 := Job{
		ID:        "job-2",
		Kind:      "echo",
		Host:      "host2",
		State:     "pending",
		CreatedAt: now.Add(time.Second),
		UpdatedAt: now.Add(time.Second),
	}
	if err := store.Create(j2); err != nil {
		t.Fatalf("Create j2: %v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List length: got %d, want 2", len(list))
	}
	if list[0].ID != "job-2" || list[1].ID != "job-1" {
		t.Errorf("List sort order wrong: %s, %s", list[0].ID, list[1].ID)
	}

	if err := store.AppendLog("job-1", "line 1\n"); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if err := store.AppendLog("job-1", "line 2\n"); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	logData, err := os.ReadFile(filepath.Join(dir, "jobs", "job-1.log"))
	if err != nil {
		t.Fatalf("ReadFile log: %v", err)
	}
	if string(logData) != "line 1\nline 2\n" {
		t.Errorf("Log content mismatch: %q", string(logData))
	}
}

func TestFileStore_ConcurrentAppendLog(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	j := Job{ID: "job-concurrent"}
	if err := store.Create(j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var wg sync.WaitGroup
	const numGoroutines = 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			line := "this is a log line from goroutine " + string(rune('A'+n)) + " with some extra padding to make it longer\n"
			if err := store.AppendLog(j.ID, line); err != nil {
				t.Errorf("AppendLog: %v", err)
			}
		}(i)
	}

	wg.Wait()

	logData, err := os.ReadFile(filepath.Join(dir, "jobs", j.ID+".log"))
	if err != nil {
		t.Fatalf("ReadFile log: %v", err)
	}

	lines := 0
	for _, b := range logData {
		if b == '\n' {
			lines++
		}
	}
	if lines != numGoroutines {
		t.Errorf("Expected %d lines, got %d", numGoroutines, lines)
	}
}
