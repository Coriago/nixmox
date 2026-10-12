package statefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateAtomicState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	if err := Write(path, map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := Write(path, make(chan int)); err == nil {
		t.Fatal("expected marshal failure")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failure modified state")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
