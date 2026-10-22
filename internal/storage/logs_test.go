package storage

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestLogRetentionAndReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), MaxLogAppend)
	for i := 0; i < LogRetention/MaxLogAppend+1; i++ {
		if _, err := s.AppendLog("task_test", chunk); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReadLog("task_test")
	if err != nil {
		t.Fatal(err)
	}
	if l.Start != MaxLogAppend || len(l.Data) != LogRetention || l.End() != LogRetention+MaxLogAppend {
		t.Fatalf("start=%d end=%d length=%d", l.Start, l.End(), len(l.Data))
	}
	if _, err := s.AppendLog("task_test", make([]byte, MaxLogAppend+1)); err == nil {
		t.Fatal("oversized log chunk accepted")
	}
	if _, err := s.ReadLog("../identity"); err == nil {
		t.Fatal("unsafe path accepted")
	}
}
