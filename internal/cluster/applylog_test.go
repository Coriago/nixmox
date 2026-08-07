package cluster

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRecordStore(t *testing.T) {
	s, err := NewLocalRecordStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.Latest("node1"); err != nil || ok {
		t.Errorf("empty store Latest: ok=%v err=%v", ok, err)
	}

	r1 := Record{Timestamp: time.Now(), Status: "success", GitSHA: "aaa", GenBefore: 1, GenAfter: 2, JobID: "j1"}
	if err := s.Record("node1", r1); err != nil {
		t.Fatal(err)
	}
	latest, ok, err := s.Latest("node1")
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if latest.GitSHA != "aaa" || latest.GenAfter != 2 {
		t.Errorf("latest mismatch: %+v", latest)
	}

	r2 := Record{Timestamp: time.Now(), Status: "failed", GitSHA: "bbb", GenBefore: 2, GenAfter: 2, JobID: "j2"}
	s.Record("node1", r2)
	hist, err := s.History("node1")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].GitSHA != "bbb" || hist[1].GitSHA != "aaa" {
		t.Errorf("history order (want newest first): %+v", hist)
	}

	for i := 0; i < 60; i++ {
		s.Record("node1", Record{Timestamp: time.Now(), Status: "success", JobID: "bulk"})
	}
	hist, _ = s.History("node1")
	if len(hist) != 50 {
		t.Errorf("prune: want 50, got %d", len(hist))
	}

	s2, err := NewLocalRecordStore(filepath.Dir(s.dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s2.Latest("node1"); !ok {
		t.Error("records did not persist across store reopen")
	}
}

func TestApplyLogFormat(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewLocalRecordStore(dir)
	s.Record("n", Record{Timestamp: time.Now(), Status: "success", GitSHA: "x", GenBefore: 3, GenAfter: 4, JobID: "j"})

	f, err := os.Open(filepath.Join(dir, "applylog", "n.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if !strings.Contains(sc.Text(), `"gen_after":4`) {
			t.Errorf("line missing gen_after: %s", sc.Text())
		}
		lines++
	}
	if lines != 1 {
		t.Errorf("want 1 JSONL line, got %d", lines)
	}
}
