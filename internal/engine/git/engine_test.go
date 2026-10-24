package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	githttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/storage/memory"

	"nixmox/internal/engine"

	"nixmox/internal/domain"
)

func gitFixture(t *testing.T) (string, *Engine, domain.Repository, domain.Commit) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	// Fixture creation and every adapter operation must work without Git installed.
	t.Setenv("PATH", t.TempDir())
	git := &Engine{Root: filepath.Join(root, "node-a")}
	source := filepath.Join(root, "origin-work")
	remote := filepath.Join(root, "remote.git")
	r, err := gogit.PlainInit(source, false, gogit.WithDefaultBranch("refs/heads/main"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := os.WriteFile(filepath.Join(source, "base"), []byte("remote base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tree, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Add("base"); err != nil {
		t.Fatal(err)
	}
	author := &object.Signature{Name: "Nixmox", Email: "nixmox@localhost", When: time.Unix(1, 0)}
	if _, err := tree.Commit("base", &gogit.CommitOptions{Author: author, Committer: author}); err != nil {
		t.Fatal(err)
	}
	bare, err := gogit.PlainInit(remote, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := bare.Close(); err != nil {
		t.Fatal(err)
	}
	upstream, err := r.CreateRemote(&config.RemoteConfig{Name: "upstream", URLs: []string{"file://" + remote}})
	if err != nil {
		t.Fatal(err)
	}
	if err := upstream.PushContext(ctx, &gogit.PushOptions{RemoteName: "upstream", RefSpecs: []config.RefSpec{"refs/heads/main:refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{Record: domain.Record{ID: "repo"}, Remote: domain.Remote{URL: "file://" + remote}, Upstream: domain.Upstream{Branch: "main"}}
	base, err := git.Fetch(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	return root, git, repo, base
}

func TestGitPinnedSourceAndPublication(t *testing.T) {
	ctx := context.Background()
	root, git, repo, base := gitFixture(t)
	peer := &Engine{Root: filepath.Join(root, "node-b")}
	if _, err := peer.Fetch(ctx, repo); err != nil {
		t.Fatal(err)
	}
	work, err := git.Materialize(ctx, domain.Source{RepositoryID: repo.ID, Commit: base})
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	content := []byte{0, 1, 2, 255, 10}
	hash := sha256.Sum256(content)
	if err = os.WriteFile(filepath.Join(work.Path(), "new.bin"), content, 0644); err != nil {
		t.Fatal(err)
	}
	commit, err := git.Commit(ctx, work, []domain.FileManifest{{Path: "new.bin", Bytes: int64(len(content)), SHA256: domain.Digest(hex.EncodeToString(hash[:]))}}, "local change")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Materialize(ctx, domain.Source{RepositoryID: repo.ID, Commit: commit}); err == nil {
		t.Fatal("unpublished source silently substituted")
	}
	publication, err := git.Push(ctx, repo, commit, "change/example")
	if err != nil || publication.State != "pushed" {
		t.Fatalf("publication: %+v %v", publication, err)
	}
	published := repo
	published.Upstream.Branch = "change/example"
	if got, err := peer.Fetch(ctx, published); err != nil || got != commit {
		t.Fatalf("published source: %s %v", got, err)
	}
	received, err := peer.Materialize(ctx, domain.Source{RepositoryID: repo.ID, Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	defer received.Close()
	got, err := os.ReadFile(filepath.Join(received.Path(), "new.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("binary source changed: %v", err)
	}
	observed, err := git.ObservePublication(ctx, domain.Publication{RepositoryID: repo.ID, RemoteURL: repo.Remote.URL, RemoteBranch: "main", Commit: base})
	if err != nil || observed.State != "pushed" {
		t.Fatal("publication changed official upstream", err)
	}
	if _, err = git.Materialize(ctx, domain.Source{RepositoryID: repo.ID, Commit: "main"}); err == nil {
		t.Fatal("mutable selector materialized")
	}
}

func draft(t *testing.T, g *Engine, repo domain.Repository, commit domain.Commit) engine.Worktree {
	t.Helper()
	work, err := g.Materialize(context.Background(), domain.Source{RepositoryID: repo.ID, Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := work.Close(); err != nil {
			t.Error(err)
		}
		for _, path := range []string{work.Path(), filepath.Join(g.directory(repo.ID), "worktrees", filepath.Base(work.Path()))} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("worktree cleanup left %s: %v", path, err)
			}
		}
	})
	return work
}

func reviewedFile(t *testing.T, work engine.Worktree, path, content string, mode os.FileMode) domain.FileManifest {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work.Path(), path), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return domain.FileManifest{Path: path, Bytes: int64(len(content)), SHA256: domain.Digest(hex.EncodeToString(sum[:]))}
}

func TestGitLibraryOperations(t *testing.T) {
	ctx := context.Background()
	_, g, repo, base := gitFixture(t)
	peer := &Engine{Root: t.TempDir()}
	if got, err := peer.Fetch(ctx, repo); err != nil || got != base {
		t.Fatalf("fresh in-process fetch: %s %v", got, err)
	}
	work := draft(t, g, repo, base)
	isolated := draft(t, g, repo, base)
	draft(t, peer, repo, base)
	if got, err := g.Fetch(ctx, repo); err != nil || got != base {
		t.Fatalf("repeat fetch: %s %v", got, err)
	}
	file := reviewedFile(t, work, "literal[1]*.sh", "#!/bin/sh\nexit 0\n", 0755)
	reviewedFile(t, work, "unreviewed", "keep private", 0644)
	if err := os.Remove(filepath.Join(work.Path(), "base")); err != nil {
		t.Fatal(err)
	}
	commit, err := g.Commit(ctx, work, []domain.FileManifest{file, {Path: "base"}}, "reviewed only")
	if err != nil || commit != work.Commit() {
		t.Fatalf("commit: %s %v", commit, err)
	}
	r, err := g.open(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	object, err := r.CommitObject(plumbing.NewHash(string(commit)))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := object.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 1 || tree.Entries[0].Name != file.Path || tree.Entries[0].Mode != filemode.Executable {
		t.Fatalf("unreviewed files, deletion, literal path or executable mode changed: %+v", tree.Entries)
	}
	if len(object.ParentHashes) != 1 || object.ParentHashes[0].String() != string(base) || object.Author.Name != "Nixmox" {
		t.Fatalf("commit ancestry/identity changed: %+v", object)
	}
	if _, err = os.Stat(filepath.Join(isolated.Path(), "base")); err != nil || isolated.Commit() != base {
		t.Fatal("another worktree was changed")
	}
	for range 2 {
		p, err := g.Push(ctx, repo, commit, "change/library")
		if err != nil || p.State != "pushed" {
			t.Fatalf("repeat push: %+v %v", p, err)
		}
		observer := &Engine{}
		if observed, err := observer.ObservePublication(ctx, p); err != nil || observed.State != "pushed" {
			t.Fatalf("cache-independent observation: %+v %v", observed, err)
		}
	}
	if _, err = g.Push(ctx, repo, base, "change/library"); err == nil {
		t.Fatal("non-fast-forward push permitted")
	}
	p, err := g.ObservePublication(ctx, domain.Publication{RepositoryID: repo.ID, RemoteURL: repo.Remote.URL, RemoteBranch: "change/library", Commit: base})
	if err != nil || p.State != "diverged" || p.RemoteHead == nil || *p.RemoteHead != commit {
		t.Fatalf("rejected push overwrote remote: %+v %v", p, err)
	}
}

func TestGitReviewedFiles(t *testing.T) {
	ctx := context.Background()
	_, g, repo, base := gitFixture(t)
	work := draft(t, g, repo, base)
	file := reviewedFile(t, work, "new", "reviewed", 0644)
	for _, files := range [][]domain.FileManifest{
		{file, file}, {{Path: "../escape"}}, {{Path: ".git/config"}}, {{Path: "new", Bytes: -1}},
		{{Path: "new", Bytes: file.Bytes, SHA256: domain.Digest(strings.Repeat("0", 64))}},
	} {
		if _, err := g.Commit(ctx, work, files, "invalid"); err == nil {
			t.Fatalf("accepted invalid manifest: %+v", files)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work.Path(), "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, work, []domain.FileManifest{{Path: "link"}}, "symlink"); err == nil {
		t.Fatal("symlink accepted")
	}
	r, err := gogit.PlainOpen(work.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	tree, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	reviewedFile(t, work, "staged", "not reviewed", 0644)
	if _, err = tree.Add("staged"); err != nil {
		t.Fatal(err)
	}
	if _, err = g.Commit(ctx, work, []domain.FileManifest{file}, "must fail"); err == nil {
		t.Fatal("unreviewed staged changes committed")
	}
	if work.Commit() != base {
		t.Fatal("rejected review changed the draft head")
	}
}

func TestGitRemoteCredentialsAndCancellation(t *testing.T) {
	ctx := context.Background()
	g := &Engine{Root: t.TempDir()}
	for _, raw := range []string{"http://example/repo", "ext::tool", "https://user:secret@example/repo", "https://example/repo?token=secret", "file://remote/path", "file:relative"} {
		repo := domain.Repository{Remote: domain.Remote{URL: raw}}
		_, _, cleanup, err := g.remote(ctx, repo, memory.NewStorage())
		cleanup()
		if err == nil {
			t.Fatalf("unsafe remote accepted: %s", raw)
		}
	}
	repo := domain.Repository{Record: domain.Record{ID: "repo"}, Remote: domain.Remote{URL: "ssh://git@example/repo"}, Upstream: domain.Upstream{Branch: "main"}}
	if _, err := g.Fetch(ctx, repo); err == nil {
		t.Fatal("SSH permitted ambient authentication")
	}
	credential := domain.ID("credential")
	repo.Remote = domain.Remote{URL: "https://example/repo", CredentialRef: &credential}
	if _, err := g.Fetch(ctx, repo); err == nil {
		t.Fatal("missing credential holder accepted")
	}
	for _, fail := range []bool{false, true} {
		released := false
		want := &githttp.BasicAuth{Username: "git", Password: "test-only"}
		g.Credentials = func(_ context.Context, got domain.Repository) (Authentication, func(), error) {
			if got.ID != repo.ID || *got.Remote.CredentialRef != credential {
				t.Fatal("credential scope changed")
			}
			var err error
			if fail {
				err = errors.New("holder unavailable")
			}
			return Authentication{HTTP: want}, func() { released = true }, err
		}
		_, auth, cleanup, err := g.remote(ctx, repo, memory.NewStorage())
		cleanup()
		if !released || (err != nil) != fail || (!fail && auth.HTTP != want) {
			t.Fatalf("credential lifecycle: released=%v err=%v", released, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := g.Fetch(cancelled, repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled fetch: %v", err)
	}
	for _, branch := range []string{"", "-force", "main:refs/heads/other", "../main", "main\n"} {
		if _, err := branchName(branch); err == nil {
			t.Fatalf("unsafe branch accepted: %q", branch)
		}
	}
	if _, err := g.Materialize(ctx, domain.Source{RepositoryID: repo.ID, Commit: domain.Commit(strings.Repeat("a", 64))}); err == nil {
		t.Fatal("SHA-256 commit silently truncated")
	}
}
