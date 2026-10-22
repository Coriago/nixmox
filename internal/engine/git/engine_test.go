package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nixmox/internal/domain"
	"nixmox/internal/engine/task"
)

func TestGitIncrementalSourceAndPublication(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git := &Engine{Root: filepath.Join(root, "node-a"), Runner: task.ProcessRunner{}}
	source := filepath.Join(root, "origin-work")
	remote := filepath.Join(root, "remote.git")
	run := func(dir string, args ...string) []byte {
		out, err := git.run(ctx, dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	run("", "init", "-b", "main", source)
	if err := os.WriteFile(filepath.Join(source, "base"), []byte("remote base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(source, "add", "base")
	run(source, "commit", "-m", "base")
	run("", "init", "--bare", remote)
	run(source, "push", remote, "main")
	repo := domain.Repository{Record: domain.Record{ID: "repo"}, Remote: domain.Remote{URL: "file://" + remote}, Upstream: domain.Upstream{Branch: "main"}}
	base, err := git.Fetch(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	peer := &Engine{Root: filepath.Join(root, "node-b"), Runner: task.ProcessRunner{}}
	if _, err = peer.Fetch(ctx, repo); err != nil {
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
	var packed bytes.Buffer
	bundle, err := git.Bundle(ctx, repo, commit, []domain.Commit{base}, &packed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = git.Bundle(ctx, repo, commit, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("full bundle permitted")
	}
	if err = peer.Import(ctx, repo, bundle, bytes.NewReader(packed.Bytes())); err != nil {
		t.Fatal(err)
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
	empty := &Engine{Root: filepath.Join(root, "node-c"), Runner: task.ProcessRunner{}}
	if err = empty.Import(ctx, repo, bundle, bytes.NewReader(packed.Bytes())); err == nil {
		t.Fatal("missing remote base was silently substituted")
	}
	publication, err := git.Push(ctx, repo, commit, "change/example")
	if err != nil || publication.State != "pushed" {
		t.Fatalf("publication: %+v %v", publication, err)
	}
	head := run("", "ls-remote", "file://"+remote, "refs/heads/main")
	if !strings.HasPrefix(string(head), string(base)) {
		t.Fatal("publication changed official upstream")
	}
	if _, err = git.Materialize(ctx, domain.Source{RepositoryID: repo.ID, Commit: "main"}); err == nil {
		t.Fatal("mutable selector materialized")
	}
}
