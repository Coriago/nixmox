package gitstore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIncrementRetryPreservesCommitAndRejectsSymlinkWrites(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git integration requires the dev shell")
	}
	remote := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = remote
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(remote, "base"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	g, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	head, err := g.Fetch(ctx, "r_test", remote, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"local/binary": {0, 1, 255, 10}, "local/text": []byte("exact\n")}
	at := time.Unix(1000000000, 0)
	first, err := g.Commit(ctx, "r_test", head, head, "cmd_change", files, at)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := g.Commit(ctx, "r_test", head, head, "cmd_change", files, at)
	if err != nil || retry.Commit != first.Commit || retry.Digest != first.Digest {
		t.Fatal("non-deterministic retry", first, retry, err)
	}
	dir := t.TempDir()
	external := t.TempDir()
	if err = os.Symlink(external, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if WriteFile(dir, "escape/file", []byte("bad")) == nil {
		t.Fatal("symlink escape allowed")
	}
	if _, err = os.Stat(filepath.Join(external, "file")); !os.IsNotExist(err) {
		t.Fatal("outside write", err)
	}
	diff, err := g.Diff(ctx, "r_test", head, first.Commit)
	if err != nil || !strings.Contains(diff, "local/text") {
		t.Fatal("diff unavailable", err)
	}
}
