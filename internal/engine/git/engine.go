// Package git implements isolated Git materialization, incremental transport and explicit publication.
package git

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
	"nixmox/internal/engine/task"
)

var _ engine.Git = (*Engine)(nil)

type Engine struct {
	Root   string
	Runner engine.Runner
	// Holder-owned credentials are injected ephemerally. No ambient SSH agent,
	// credential helper, user config or interactive prompt is used.
	Credentials func(context.Context, domain.Repository, *engine.CommandLine) (func(), error)
}

func (g *Engine) directory(id domain.ID) string {
	return filepath.Join(g.Root, "repositories", shared.Token(id))
}

func (g *Engine) plan(directory string, args ...string) engine.CommandLine {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "protocol.allow=never", "-c", "protocol.file.allow=always", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always", "-c", "core.fsync=committed", "-c", "core.fsyncMethod=fsync", "-c", "transfer.fsckObjects=true"}
	if directory != "" {
		base = append(base, "-C", directory)
	}
	return engine.CommandLine{Program: "git", Args: append(base, args...), Environment: map[string]string{"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0", "GIT_SSH_COMMAND": "ssh -F /dev/null -oBatchMode=yes -oIdentityAgent=none -oIdentitiesOnly=yes", "GIT_AUTHOR_NAME": "Nixmox", "GIT_AUTHOR_EMAIL": "nixmox@localhost", "GIT_COMMITTER_NAME": "Nixmox", "GIT_COMMITTER_EMAIL": "nixmox@localhost"}}
}

func (g *Engine) run(ctx context.Context, directory string, args ...string) ([]byte, error) {
	return task.Capture(ctx, g.Runner, g.plan(directory, args...), domain.QueryTotalLimit)
}

func validRemote(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme != "https" && u.Scheme != "ssh" && u.Scheme != "file" {
		return false
	}
	if u.User != nil {
		if _, password := u.User.Password(); password || u.Scheme != "ssh" {
			return false
		}
	}
	return (u.Scheme == "file" && filepath.IsAbs(u.Path)) || (u.Host != "")
}

func (g *Engine) remote(ctx context.Context, repository domain.Repository, args ...string) ([]byte, error) {
	if !validRemote(repository.Remote.URL) {
		return nil, errors.New("remote requires HTTPS, SSH or absolute file URL without embedded secrets")
	}
	plan := g.plan(g.directory(repository.ID), args...)
	if repository.Remote.CredentialRef != nil {
		if g.Credentials == nil {
			return nil, &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "Git credential holder unavailable"}
		}
		cleanup, err := g.Credentials(ctx, repository, &plan)
		if err != nil {
			return nil, err
		}
		if cleanup != nil {
			defer cleanup()
		}
	}
	return task.Capture(ctx, g.Runner, plan, domain.QueryTotalLimit)
}

func (g *Engine) ensure(ctx context.Context, id domain.ID) error {
	if !filepath.IsAbs(g.Root) || id == "" {
		return errors.New("absolute Git state root and repository ID required")
	}
	dir := g.directory(id)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	_, err := g.run(ctx, "", "init", "--bare", dir)
	return err
}

func (g *Engine) branch(ctx context.Context, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, "\x00\n") {
		return errors.New("invalid branch")
	}
	_, err := g.run(ctx, "", "check-ref-format", "refs/heads/"+branch)
	return err
}

func (g *Engine) Fetch(ctx context.Context, repository domain.Repository) (domain.Commit, error) {
	if err := g.ensure(ctx, repository.ID); err != nil {
		return "", err
	}
	if err := g.branch(ctx, repository.Upstream.Branch); err != nil {
		return "", err
	}
	ref := "refs/remotes/upstream/" + repository.Upstream.Branch
	if _, err := g.remote(ctx, repository, "fetch", "--no-tags", "--no-recurse-submodules", repository.Remote.URL, "+refs/heads/"+repository.Upstream.Branch+":"+ref); err != nil {
		return "", err
	}
	out, err := g.run(ctx, g.directory(repository.ID), "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	commit := domain.Commit(strings.TrimSpace(string(out)))
	if !shared.ValidCommit(commit) {
		return "", errors.New("invalid fetched commit")
	}
	return commit, nil
}

type gitWorktree struct {
	path       string
	commit     domain.Commit
	repository domain.ID
	engine     *Engine
}

func (w *gitWorktree) Path() string { return w.path }

func (w *gitWorktree) Commit() domain.Commit { return w.commit }

func (w *gitWorktree) Close() error {
	_, err := w.engine.run(context.Background(), w.engine.directory(w.repository), "worktree", "remove", "--force", w.path)
	return err
}

func (g *Engine) Materialize(ctx context.Context, source domain.Source) (engine.Worktree, error) {
	if !shared.ValidCommit(source.Commit) || source.RepositoryID == "" {
		return nil, errors.New("pinned source required")
	}
	dir := g.directory(source.RepositoryID)
	if _, err := g.run(ctx, dir, "cat-file", "-e", string(source.Commit)+"^{commit}"); err != nil {
		return nil, &domain.Error{Code: "SOURCE_UNAVAILABLE", Message: "Pinned base or retained increment is not materialized"}
	}
	worktrees := filepath.Join(g.Root, "worktrees")
	if err := os.MkdirAll(worktrees, 0700); err != nil {
		return nil, err
	}
	path, err := os.MkdirTemp(worktrees, "draft-")
	if err != nil {
		return nil, err
	}
	if _, err = g.run(ctx, dir, "worktree", "add", "--detach", path, string(source.Commit)); err != nil {
		os.RemoveAll(path)
		return nil, err
	}
	return &gitWorktree{path, source.Commit, source.RepositoryID, g}, nil
}

func relativeFile(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\\\x00") {
		return false
	}
	for _, p := range strings.Split(path, "/") {
		if p == ".." || p == ".git" {
			return false
		}
	}
	return path != "."
}

func (g *Engine) Commit(ctx context.Context, worktree engine.Worktree, files []domain.FileManifest, message string) (domain.Commit, error) {
	if len(files) == 0 || message == "" {
		return "", errors.New("reviewed files and commit message required")
	}
	w, ok := worktree.(*gitWorktree)
	if !ok || w.engine != g {
		return "", errors.New("worktree is not owned by this Git engine")
	}
	root, err := os.OpenRoot(w.Path())
	if err != nil {
		return "", err
	}
	defer root.Close()
	paths := []string{}
	seen := map[string]bool{}
	for _, file := range files {
		if !relativeFile(file.Path) || seen[file.Path] {
			return "", errors.New("unsafe/duplicate reviewed path")
		}
		seen[file.Path] = true
		paths = append(paths, file.Path)
		info, e := root.Lstat(file.Path)
		if errors.Is(e, os.ErrNotExist) && file.Bytes == 0 && file.SHA256 == "" {
			continue
		} // Reviewed deletion.
		if e != nil {
			return "", e
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("reviewed file must be regular, not a symlink")
		}
		f, e := root.Open(file.Path)
		if e != nil {
			return "", e
		}
		hash := sha256.New()
		n, e := io.Copy(hash, io.LimitReader(f, file.Bytes+1))
		f.Close()
		if e != nil || n != file.Bytes || hex.EncodeToString(hash.Sum(nil)) != string(file.SHA256) {
			return "", errors.New("reviewed file digest changed")
		}
	}
	staged, err := g.run(ctx, w.Path(), "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return "", err
	}
	if len(staged) > 0 {
		return "", errors.New("worktree already has staged changes; reconcile first")
	}
	if _, err = g.run(ctx, w.Path(), append([]string{"add", "--all", "--"}, paths...)...); err != nil {
		return "", err
	}
	if _, err = g.run(ctx, w.Path(), "commit", "--no-gpg-sign", "-m", message); err != nil {
		return "", err
	}
	out, err := g.run(ctx, w.Path(), "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	commit := domain.Commit(strings.TrimSpace(string(out)))
	if !shared.ValidCommit(commit) {
		return "", errors.New("invalid commit result")
	}
	w.commit = commit
	return commit, nil
}

func (g *Engine) Bundle(ctx context.Context, repository domain.Repository, commit domain.Commit, prerequisites []domain.Commit, writer io.Writer) (domain.Bundle, error) {
	if !shared.ValidCommit(commit) || len(prerequisites) == 0 || len(prerequisites) > 128 {
		return domain.Bundle{}, errors.New("incremental bundle requires pinned prerequisites")
	}
	dir := g.directory(repository.ID)
	for _, base := range prerequisites {
		if !shared.ValidCommit(base) || base == commit {
			return domain.Bundle{}, errors.New("invalid bundle prerequisite")
		}
		if _, err := g.run(ctx, dir, "merge-base", "--is-ancestor", string(base), string(commit)); err != nil {
			return domain.Bundle{}, errors.New("bundle prerequisite is not an available ancestor")
		}
	}
	temp, err := os.MkdirTemp(g.Root, "bundle-")
	if err != nil {
		return domain.Bundle{}, err
	}
	defer os.RemoveAll(temp)
	ref := "refs/nixmox/bundles/" + filepath.Base(temp)
	if _, err = g.run(ctx, dir, "update-ref", ref, string(commit), strings.Repeat("0", len(commit))); err != nil {
		return domain.Bundle{}, err
	}
	defer g.run(context.Background(), dir, "update-ref", "-d", ref, string(commit))
	filename := filepath.Join(temp, "increment.bundle")
	args := []string{"bundle", "create", filename, ref}
	for _, base := range prerequisites {
		args = append(args, "^"+string(base))
	}
	if _, err = g.run(ctx, dir, args...); err != nil {
		return domain.Bundle{}, err
	}
	f, err := os.Open(filename)
	if err != nil {
		return domain.Bundle{}, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(writer, hash), f)
	if err != nil {
		return domain.Bundle{}, err
	}
	digest := domain.Digest(hex.EncodeToString(hash.Sum(nil)))
	return domain.Bundle{ObjectKey: string(repository.ID) + "/" + string(digest), SHA256: digest, Bytes: n}, nil
}

func bundleHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReader(io.LimitReader(f, 64<<10))
	first, err := reader.ReadString('\n')
	if err != nil || (first != "# v2 git bundle\n" && first != "# v3 git bundle\n") {
		return errors.New("unsupported bundle header")
	}
	prerequisites := 0
	for {
		line, e := reader.ReadString('\n')
		if e != nil {
			return errors.New("invalid bundle header")
		}
		if line == "\n" {
			break
		}
		if strings.HasPrefix(line, "-") {
			parts := strings.Fields(line[1:])
			if len(parts) == 0 || !shared.ValidCommit(domain.Commit(parts[0])) {
				return errors.New("invalid prerequisite")
			}
			prerequisites++
		}
	}
	if prerequisites == 0 {
		return errors.New("full base bundles are forbidden")
	}
	return nil
}

func (g *Engine) Import(ctx context.Context, repository domain.Repository, bundle domain.Bundle, reader io.Reader) error {
	if !shared.ValidBundle(bundle) {
		return errors.New("invalid bundle manifest")
	}
	if err := g.ensure(ctx, repository.ID); err != nil {
		return err
	}
	file, err := os.CreateTemp(g.Root, "import-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, bundle.Bytes+1))
	if err != nil {
		return err
	}
	if n != bundle.Bytes || hex.EncodeToString(hash.Sum(nil)) != string(bundle.SHA256) {
		return errors.New("bundle content differs from manifest")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = bundleHeader(file.Name()); err != nil {
		return err
	}
	dir := g.directory(repository.ID)
	if _, err = g.run(ctx, dir, "bundle", "verify", file.Name()); err != nil {
		return &domain.Error{Code: "SOURCE_UNAVAILABLE", Message: "Bundle prerequisites missing or invalid"}
	}
	if _, err = g.run(ctx, dir, "bundle", "unbundle", file.Name()); err != nil {
		return err
	}
	_, err = g.run(ctx, dir, "fsck", "--connectivity-only", "--no-dangling")
	return err
}

func (g *Engine) Rebase(ctx context.Context, worktree engine.Worktree, onto domain.Commit) ([]domain.FileChange, error) {
	w, ok := worktree.(*gitWorktree)
	if !ok || w.engine != g || !shared.ValidCommit(onto) {
		return nil, errors.New("owned worktree and pinned rebase target required")
	}
	_, err := g.run(ctx, w.Path(), "rebase", string(onto))
	if err == nil {
		out, readErr := g.run(ctx, w.Path(), "rev-parse", "--verify", "HEAD")
		if readErr != nil {
			return nil, readErr
		}
		commit := domain.Commit(strings.TrimSpace(string(out)))
		if !shared.ValidCommit(commit) {
			return nil, errors.New("invalid rebased commit")
		}
		w.commit = commit
		return []domain.FileChange{}, nil
	}
	conflicts, e := g.run(ctx, w.Path(), "diff", "--name-only", "--diff-filter=U", "-z")
	if e != nil || len(conflicts) == 0 {
		return nil, err
	}
	files := []domain.FileChange{}
	for _, path := range strings.Split(strings.TrimSuffix(string(conflicts), "\x00"), "\x00") {
		files = append(files, domain.FileChange{Path: path, Change: "conflict"})
	}
	return files, &domain.Error{Code: "REBASE_CONFLICT", Message: "Conflicts retained in the private worktree"}
}

func (g *Engine) Push(ctx context.Context, repository domain.Repository, commit domain.Commit, branch string) (domain.Publication, error) {
	result := domain.Publication{RepositoryID: repository.ID, Commit: commit, RemoteBranch: branch, RemoteURL: repository.Remote.URL, CredentialRef: repository.Remote.CredentialRef, State: "unknown"}
	if !shared.ValidCommit(commit) {
		return result, errors.New("publication requires pinned commit")
	}
	if err := g.branch(ctx, branch); err != nil {
		return result, err
	}
	_, err := g.remote(ctx, repository, "push", "--porcelain", "--no-verify", repository.Remote.URL, string(commit)+":refs/heads/"+branch)
	if err != nil {
		return result, &domain.Error{Code: "PUBLICATION_UNKNOWN", Message: "Push outcome requires remote observation before retry"}
	}
	return g.ObservePublication(ctx, result)
}

func (g *Engine) ObservePublication(ctx context.Context, p domain.Publication) (domain.Publication, error) {
	repository := domain.Repository{Record: domain.Record{ID: p.RepositoryID}, Remote: domain.Remote{URL: p.RemoteURL, CredentialRef: p.CredentialRef}}
	if err := g.branch(ctx, p.RemoteBranch); err != nil {
		return p, err
	}
	out, err := g.remote(ctx, repository, "ls-remote", "--exit-code", p.RemoteURL, "refs/heads/"+p.RemoteBranch)
	if err != nil {
		return p, err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || !shared.ValidCommit(domain.Commit(fields[0])) {
		return p, fmt.Errorf("unexpected publication observation")
	}
	head := domain.Commit(fields[0])
	p.RemoteHead = &head
	p.State = "diverged"
	if head == p.Commit {
		p.State = "pushed"
	}
	return p, nil
}
