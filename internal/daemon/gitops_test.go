package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nixmox/internal/config"
)

// Optional live check: a fresh installer state must fetch and pin the actual
// public repository without inheriting the user's Git or SSH configuration.
func TestLiveInstallerPublicGitHubPull(t *testing.T) {
	url := os.Getenv("NIXMOX_TEST_PUBLIC_GIT_URL")
	sha := os.Getenv("NIXMOX_TEST_PUBLIC_GIT_SHA")
	if url == "" || sha == "" {
		t.Skip("set public Git URL and SHA to exercise live installer pull")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_SSH_COMMAND", "false")
	t.Setenv("SSH_AUTH_SOCK", "")
	cfg := &config.Config{DataDir: t.TempDir(), InstallerMode: true, GitRepoURL: url, GitBranch: "main"}
	m, err := GitOps(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "install")
	if err := m.PinInstall(ctx, sha, dir); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(out)) != sha {
		t.Fatal(string(out), err)
	}
	// Exercise a later fetch too: there is no one-off shell URL rewrite.
	if err := m.PinInstall(ctx, sha, dir); err != nil {
		t.Fatal(err)
	}
}
