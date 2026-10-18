package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func seedRepo(t *testing.T) (bare, work string) {
	t.Helper()
	bare = filepath.Join(t.TempDir(), "fixture.git")
	work = filepath.Join(t.TempDir(), "work")
	gitCmd(t, "", "init", "-q", "--bare", "--initial-branch=main", bare)
	gitCmd(t, "", "init", "-q", "--initial-branch=main", work)
	gitCmd(t, work, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(work, "flake.nix"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-q", "-m", "initial")
	gitCmd(t, work, "push", "-q", "origin", "main")
	return bare, work
}

func commitAndPush(t *testing.T, work, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, name), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-q", "-m", name)
	gitCmd(t, work, "push", "-q", "origin", "main")
}

func TestExecClient(t *testing.T) {
	bare, _ := seedRepo(t)
	ctx := context.Background()
	c := ExecClient{}

	cloneDir := filepath.Join(t.TempDir(), "clone")
	if err := c.Clone(ctx, bare, "main", cloneDir); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	sha, err := c.CurrentSHA(ctx, cloneDir)
	if err != nil {
		t.Fatalf("CurrentSHA: %v", err)
	}
	remote, err := c.RemoteHEAD(ctx, cloneDir, "main")
	if err != nil {
		t.Fatalf("RemoteHEAD: %v", err)
	}
	if sha != remote {
		t.Errorf("HEAD %s != origin/main %s after clone", sha, remote)
	}
	if err := c.Fetch(ctx, cloneDir); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if err := c.ResetHard(ctx, cloneDir, "main"); err != nil {
		t.Fatalf("ResetHard: %v", err)
	}
}

func TestInstallerGitHubReadTransport(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	for _, url := range []string{"git@github.com:owner/config.git", "ssh://git@github.com/owner/config.git", "https://github.com/owner/config.git", "git@private.example:owner/config.git"} {
		for _, installer := range []bool{false, true} {
			client := ExecClient{InstallerHTTPS: installer}
			got, err := client.run(context.Background(), "", "ls-remote", "--get-url", url)
			if err != nil {
				t.Fatal(err)
			}
			want := url
			if installer && (strings.HasPrefix(url, "git@github.com:") || strings.HasPrefix(url, "ssh://git@github.com/")) {
				want = "https://github.com/owner/config.git"
			}
			if got != want {
				t.Fatalf("installer=%v %s became %s; want %s", installer, url, got, want)
			}
		}
	}
}

func TestManager(t *testing.T) {
	bare, work := seedRepo(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	m := NewManager(dataDir, ExecClient{})

	if _, err := m.Ensure(ctx); err == nil {
		t.Fatal("Ensure without repo URL should error")
	}
	if err := m.Configure(bare, "main"); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	sha1, err := m.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure clone: %v", err)
	}
	if m.State().LastPulledSHA != sha1 || m.State().UpdateAvailable {
		t.Errorf("unexpected state after clone: %+v", m.State())
	}

	changed, _, err := m.CheckRemote(ctx)
	if err != nil || changed {
		t.Errorf("CheckRemote with no upstream change: changed=%v err=%v", changed, err)
	}

	commitAndPush(t, work, "second")
	changed, newSHA, err := m.CheckRemote(ctx)
	if err != nil {
		t.Fatalf("CheckRemote: %v", err)
	}
	if !changed || !m.State().UpdateAvailable {
		t.Errorf("expected change detected, state: %+v", m.State())
	}

	sha2, err := m.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure pull: %v", err)
	}
	if sha2 != newSHA || m.State().UpdateAvailable {
		t.Errorf("after pull: sha %s (want %s), state %+v", sha2, newSHA, m.State())
	}
	if sha1 == sha2 {
		t.Error("SHA did not advance after upstream commit")
	}

	m2 := NewManager(dataDir, ExecClient{})
	if err := m2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m2.State().LastPulledSHA != sha2 || m2.State().RepoURL != bare {
		t.Errorf("state.json round-trip mismatch: %+v", m2.State())
	}
}
