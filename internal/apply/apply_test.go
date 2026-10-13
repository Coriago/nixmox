package apply

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nixmox/internal/cluster"
	"nixmox/internal/config"
	"nixmox/internal/gitops"
	"nixmox/internal/jobs"
)

func TestLatestGeneration(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []int{40, 42, 41} {
		os.WriteFile(filepath.Join(dir, "system-"+strconv.Itoa(n)+"-link"), []byte("x"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "system"), []byte("x"), 0o644)

	if got := LatestGeneration(dir); got != 42 {
		t.Errorf("want 42, got %d", got)
	}
	if got := LatestGeneration(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("missing dir should yield 0, got %d", got)
	}
}

type fakeGitClient struct{}

func (fakeGitClient) Clone(ctx context.Context, url, branch, dir string) error {
	return os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
}
func (fakeGitClient) Fetch(ctx context.Context, dir string) error { return nil }
func (fakeGitClient) RemoteHEAD(ctx context.Context, dir, branch string) (string, error) {
	return "deadbeef", nil
}
func (fakeGitClient) ResetHard(ctx context.Context, dir, branch string) error { return nil }
func (fakeGitClient) CurrentSHA(ctx context.Context, dir string) (string, error) {
	return "deadbeef", nil
}

type applyFixture struct {
	mgr      *Manager
	records  *cluster.LocalRecordStore
	store    jobs.Store
	profiles string
}

func newApplyFixture(t *testing.T, autoApply bool, mode string) *applyFixture {
	t.Helper()

	binDir := t.TempDir()
	profiles := t.TempDir()
	markerLog := filepath.Join(t.TempDir(), "calls.log")

	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + markerLog + "\n" +
		"cur=$(ls " + profiles + " | sed -nE 's/system-([0-9]+)-link/\\1/p' | sort -n | tail -1)\n" +
		"touch " + profiles + "/system-$((cur+1))-link\n"
	os.WriteFile(filepath.Join(binDir, "nixos-rebuild"), []byte(script), 0o755)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	os.WriteFile(filepath.Join(profiles, "system-41-link"), []byte("x"), 0o644)

	dataDir := t.TempDir()
	cfg := &config.Config{DataDir: dataDir, AutoApply: autoApply, ApplyMode: mode}
	gitMgr := gitops.NewManager(dataDir, fakeGitClient{})
	if err := gitMgr.Configure("file:///x", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitMgr.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}

	records, err := cluster.NewLocalRecordStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := jobs.NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.NewRunner(store)
	t.Cleanup(runner.Stop)
	m := NewManager(cfg, gitMgr, records, runner, store, "node1")
	m.profilesDir = profiles
	runner.OnTerminal = m.HandleTerminal

	return &applyFixture{mgr: m, records: records, store: store, profiles: profiles}
}

func waitRecord(t *testing.T, records *cluster.LocalRecordStore) cluster.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok, _ := records.Latest("node1"); ok {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no apply record written")
	return cluster.Record{}
}

func TestApplyEmitsRecord(t *testing.T) {
	fx := newApplyFixture(t, false, "switch")

	j, err := fx.mgr.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if j.GenBefore != 41 {
		t.Errorf("GenBefore: want 41, got %d", j.GenBefore)
	}
	if j.GitSHA != "deadbeef" {
		t.Errorf("GitSHA: want deadbeef, got %s", j.GitSHA)
	}

	r := waitRecord(t, fx.records)
	if r.Status != "success" {
		t.Errorf("status: %s", r.Status)
	}
	if r.GenBefore != 41 || r.GenAfter != 42 {
		t.Errorf("gens: want 41→42, got %d→%d", r.GenBefore, r.GenAfter)
	}
	if r.JobID != j.ID {
		t.Errorf("job link: %s vs %s", r.JobID, j.ID)
	}
}

func TestRollbackEmitsRecord(t *testing.T) {
	fx := newApplyFixture(t, false, "switch")
	if _, err := fx.mgr.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitRecord(t, fx.records)

	if _, err := fx.mgr.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hist, _ := fx.records.History("node1")
		if len(hist) == 2 {
			if hist[0].GenAfter != hist[1].GenAfter+1 {
				t.Errorf("rollback gens: %+v", hist[0])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("rollback record not written")
}

func TestApplyRequiresPulledSHA(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{DataDir: dataDir}
	gitMgr := gitops.NewManager(dataDir, fakeGitClient{})
	records, _ := cluster.NewLocalRecordStore(dataDir)
	store, _ := jobs.NewFileStore(dataDir)
	m := NewManager(cfg, gitMgr, records, jobs.NewRunner(store), store, "node1")

	j, err := m.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.runner.Stop()
	got, _ := store.Get(j.ID)
	if got.State != "failed" || !strings.Contains(got.Error, "nothing pulled") {
		t.Fatal(got)
	}
}

func TestAutoApplyGate(t *testing.T) {
	off := newApplyFixture(t, false, "switch")
	off.mgr.OnNewSHA("cafe")
	if _, ok, _ := off.records.Latest("node1"); ok {
		t.Error("auto-apply disabled: no record expected")
	}

	on := newApplyFixture(t, true, "switch")
	on.mgr.OnNewSHA("cafe")
	r := waitRecord(t, on.records)
	if r.GitSHA != "deadbeef" {
		t.Errorf("auto-apply used SHA %s (last pulled), want deadbeef", r.GitSHA)
	}
}

func TestHandleTerminalIgnoresOtherKinds(t *testing.T) {
	fx := newApplyFixture(t, false, "switch")
	fx.mgr.HandleTerminal(jobs.Job{Kind: "echo", State: "applied"})
	if _, ok, _ := fx.records.Latest("node1"); ok {
		t.Error("echo jobs must not produce apply records")
	}
}

func TestApplyPinsDirectoryAndConfiguration(t *testing.T) {
	fx := newApplyFixture(t, false, "switch")
	fx.mgr.cfg.FlakeDirectory = "e2e"
	fx.mgr.cfg.FlakeAttribute = "node2"
	j, err := fx.mgr.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fx.mgr.runner.Stop()
	ref, err := url.Parse(j.Args[len(j.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	if ref.Scheme != "git+file" || ref.Query().Get("dir") != "e2e" || ref.Query().Get("rev") != "deadbeef" || ref.Fragment != "node2" {
		t.Fatal(ref)
	}
}
