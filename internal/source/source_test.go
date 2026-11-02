package source_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"nixmox/internal/source"
)

func TestPinnedWorktree(t *testing.T) {
	// --- Test setup ---
	// ------------------
	root := t.TempDir()
	remote := filepath.Join(root, "remote")
	repo, err := git.PlainInit(remote, false, git.WithDefaultBranch(plumbing.NewBranchReferenceName("main")))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	commit := func(content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(remote, "configuration.nix"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := tree.Add("configuration.nix"); err != nil {
			t.Fatal(err)
		}
		hash, err := tree.Commit("configuration", &git.CommitOptions{Author: &object.Signature{Name: "Example", Email: "example@localhost", When: time.Unix(1, 0)}})
		if err != nil {
			t.Fatal(err)
		}
		return hash.String()
	}
	first := commit("old configuration")
	latest := commit("new configuration")
	manager := source.Manager{Root: filepath.Join(root, "worktrees")}
	definition := source.Repository{ID: "repo", URL: remote, Ref: "main"}

	// --- Usage ---
	// -------------
	refreshed, err := manager.Refresh(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	work, err := manager.Checkout(context.Background(), refreshed, source.Pin{ID: "repo", Ref: "main", SHA: first})
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	content, err := work.ReadFile("configuration.nix")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(work.Path, "outside")); err != nil {
		t.Fatal(err)
	}
	_, escapeErr := work.ReadFile("outside/remote/configuration.nix")

	// --- Assertions ---
	// ------------------
	if refreshed.SHA != latest || string(content) != "old configuration" {
		t.Fatal("checkout followed mutable branch rather than pinned commit")
	}
	if escapeErr == nil {
		t.Fatal("repository symlink escaped worktree")
	}
}
