package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCheckoutStaysPinnedAcrossGitOpsPulls(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	run(source, "init", "-b", "main")
	commit := func(value string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, "host.nix"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		run(source, "add", ".")
		run(source, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", value)
		return run(source, "rev-parse", "HEAD")
	}
	old := commit("first")
	manager := NewManager(filepath.Join(root, "state"), ExecClient{})
	if err := manager.Configure(source, "main"); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(root, "install", "repo")
	if err := manager.PinInstall(context.Background(), old, checkout); err != nil {
		t.Fatal(err)
	}
	newer := commit("second")
	if sha, err := manager.Ensure(context.Background()); err != nil || sha != newer {
		t.Fatal(sha, err)
	}
	if err := manager.PinInstall(context.Background(), old, checkout); err != nil {
		t.Fatal(err)
	}
	if got := run(checkout, "rev-parse", "HEAD"); got != old {
		t.Fatal("install revision moved", got)
	}
	if err := manager.PinInstall(context.Background(), newer, checkout); err == nil {
		t.Fatal("reused pinned directory for a different revision")
	}
	other := filepath.Join(root, "retry", "repo")
	if err := manager.PinInstall(context.Background(), strings.Repeat("f", 40), other); err == nil {
		t.Fatal("accepted missing commit")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("failed pull left a partial installation checkout")
	}
	if err := manager.PinInstall(context.Background(), old, other); err != nil {
		t.Fatal("failed pull could not be retried", err)
	}
}
