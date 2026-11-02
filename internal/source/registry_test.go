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
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/host"
	"nixmox/internal/source"
	"nixmox/internal/testutil"
)

func TestConfigurationRegistry(t *testing.T) {
	// --- Test setup ---
	// ------------------
	if os.Getenv("NIXMOX_TEST_NIX") == "" {
		t.Skip("run nix develop -c just test-registry for real Nix evaluation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	for _, bucket := range []string{"CONFIGURATIONS", "REPOS"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatal(err)
		}
	}
	configurations, err := cluster.OpenStore[source.Configuration](ctx, js, "CONFIGURATIONS")
	if err != nil {
		t.Fatal(err)
	}
	repos, err := cluster.OpenStore[source.Repository](ctx, js, "REPOS")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote")
	repository, err := git.PlainInit(remote, false, git.WithDefaultBranch(plumbing.NewBranchReferenceName("main")))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	commit := func(outputs string) string {
		t.Helper()
		content := []byte("{ outputs = { self }: { configurations.nixos = { " + outputs + " }; }; }")
		if err := os.WriteFile(filepath.Join(remote, "flake.nix"), content, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := tree.Add("flake.nix"); err != nil {
			t.Fatal(err)
		}
		hash, err := tree.Commit("configurations", &git.CommitOptions{Author: &object.Signature{Name: "Example", Email: "example@localhost", When: time.Unix(1, 0)}})
		if err != nil {
			t.Fatal(err)
		}
		return hash.String()
	}
	first := commit("cedar = {};")
	repo := source.Repository{ID: "repo", URL: remote, Ref: "main", SHA: first}
	if _, err = repos.Create(ctx, repo.ID, repo); err != nil {
		t.Fatal(err)
	}
	registry := source.Registry{Sources: source.Manager{Root: filepath.Join(root, "worktrees")}, Repositories: repos, Configurations: configurations, Run: (host.Process{}).Run}

	// --- Usage ---
	// -------------
	initial, err := registry.Register(ctx, repo, ".")
	if err != nil {
		t.Fatal(err)
	}
	latest := commit("cedar = {}; birch = {};")
	repo.SHA = latest
	discovered, err := registry.Register(ctx, repo, ".")
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := configurations.Get(ctx, initial[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := registry.Refresh(ctx, initial[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if len(initial) != 1 || len(discovered) != 2 {
		t.Fatal("configuration discovery failed")
	}
	if unchanged.Value.TargetRepo.SHA != first || refreshed.TargetRepo.SHA != latest {
		t.Fatal("repository discovery moved an existing deployment pin without explicit refresh")
	}
}
