package jobs

import (
	"sync"
	"testing"
)

func TestSubmitRejectedJobIsTerminal(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(store)
	lock := &sync.Mutex{}
	r.mu.Store("host", lock)
	lock.Lock()
	defer lock.Unlock()
	if _, err = r.Submit(Job{Host: "host", Kind: "echo"}); err != ErrHostBusy {
		t.Fatal(err)
	}
	list, _ := store.List()
	if len(list) != 1 || list[0].State != "failed" {
		t.Fatal(list)
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
