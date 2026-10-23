// Package git implements isolated Git materialization, incremental transport and explicit publication.
package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

var _ engine.Git = (*Engine)(nil)

type Engine struct {
	Root string
	// Runner is needed only for operations unsupported by stable go-git.
	Runner engine.Runner
	// Credentials returns ephemeral holder-owned authentication and optional cleanup.
	// SSH requires PublicKeys with an explicit host-key verifier; never an ambient agent.
	Credentials func(context.Context, domain.Repository) (transport.AuthMethod, func(), error)
}

func (g *Engine) directory(id domain.ID) string {
	return filepath.Join(g.Root, "repositories", shared.Token(id))
}

// ValidateRepository checks transport and branch syntax without network effects.
func ValidateRepository(repository domain.Repository) error {
	if !validRemote(repository.Remote.URL) {
		return errors.New("remote requires HTTPS, SSH or absolute file URL without embedded secrets")
	}
	_, err := branchName(repository.Upstream.Branch)
	return err
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
	if u.Scheme == "file" {
		return (u.Host == "" || u.Host == "localhost") && filepath.IsAbs(u.Path)
	}
	return u.Host != ""
}

func (g *Engine) remote(ctx context.Context, repository domain.Repository, store storage.Storer) (*gogit.Remote, transport.AuthMethod, func(), error) {
	cleanup := func() {}
	if err := ctx.Err(); err != nil {
		return nil, nil, cleanup, err
	}
	if !validRemote(repository.Remote.URL) {
		return nil, nil, cleanup, errors.New("remote requires HTTPS, SSH or absolute file URL without embedded secrets")
	}
	var auth transport.AuthMethod
	if repository.Remote.CredentialRef != nil {
		if g.Credentials == nil {
			return nil, nil, cleanup, &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "Git credential holder unavailable"}
		}
		value, release, err := g.Credentials(ctx, repository)
		if release != nil {
			cleanup = release
		}
		if err != nil {
			return nil, nil, cleanup, err
		}
		if value == nil {
			return nil, nil, cleanup, &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "Git credential holder returned no authentication"}
		}
		auth = value
	}
	if strings.HasPrefix(repository.Remote.URL, "ssh:") {
		keys, ok := auth.(*gitssh.PublicKeys)
		if !ok || keys == nil || keys.Signer == nil || keys.HostKeyCallback == nil {
			return nil, nil, cleanup, &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "SSH requires a managed key and explicit host-key verification"}
		}
	}
	return gogit.NewRemote(store, &config.RemoteConfig{Name: "upstream", URLs: []string{repository.Remote.URL}}), auth, cleanup, nil
}

func (g *Engine) open(ctx context.Context, id domain.ID) (*gogit.Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(g.Root) || id == "" {
		return nil, errors.New("absolute Git state root and repository ID required")
	}
	return gogit.PlainOpen(g.directory(id))
}

func (g *Engine) ensure(ctx context.Context, id domain.ID) error {
	_, err := g.open(ctx, id)
	if !errors.Is(err, gogit.ErrRepositoryNotExists) {
		return err
	}
	dir := g.directory(id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	_, err = gogit.PlainInit(dir, true)
	return err
}

func branchName(branch string) (plumbing.ReferenceName, error) {
	ref := plumbing.NewBranchReferenceName(branch)
	return ref, ref.Validate()
}

// ValidCommit validates the exact object ID supported by this adapter.
func ValidCommit(commit domain.Commit) bool { return validCommit(commit) }

// Stable go-git stores SHA-1 objects. Never truncate a SHA-256 object ID into a
// different commit; support for that repository format needs a separate upgrade.
func validCommit(commit domain.Commit) bool {
	return len(commit) == 40 && shared.ValidCommit(commit)
}

func (g *Engine) Fetch(ctx context.Context, repository domain.Repository) (domain.Commit, error) {
	branch, err := branchName(repository.Upstream.Branch)
	if err != nil {
		return "", err
	}
	if err = g.ensure(ctx, repository.ID); err != nil {
		return "", err
	}
	r, err := g.open(ctx, repository.ID)
	if err != nil {
		return "", err
	}
	remote, auth, cleanup, err := g.remote(ctx, repository, r.Storer)
	defer cleanup()
	if err != nil {
		return "", err
	}
	ref := plumbing.ReferenceName("refs/remotes/upstream/" + repository.Upstream.Branch)
	err = remote.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: "upstream", Auth: auth, Tags: gogit.NoTags,
		RefSpecs: []config.RefSpec{config.RefSpec("+" + branch.String() + ":" + ref.String())},
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return "", err
	}
	head, err := r.Reference(ref, true)
	if err != nil {
		return "", err
	}
	if _, err = r.CommitObject(head.Hash()); err != nil {
		return "", err
	}
	return domain.Commit(head.Hash().String()), nil
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
	if !validCommit(source.Commit) || source.RepositoryID == "" {
		return nil, errors.New("pinned source required")
	}
	r, err := g.open(ctx, source.RepositoryID)
	if errors.Is(err, gogit.ErrRepositoryNotExists) {
		return nil, &domain.Error{Code: "SOURCE_UNAVAILABLE", Message: "Repository base is not materialized"}
	}
	if err != nil {
		return nil, err
	}
	if _, err := r.CommitObject(plumbing.NewHash(string(source.Commit))); err != nil {
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
	if _, err = g.run(ctx, g.directory(source.RepositoryID), "worktree", "add", "--detach", path, string(source.Commit)); err != nil {
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r, err := gogit.PlainOpenWithOptions(w.Path(), &gogit.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return "", err
	}
	tree, err := r.Worktree()
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(w.Path())
	if err != nil {
		return "", err
	}
	defer root.Close()
	paths := []string{}
	seen := map[string]bool{}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !relativeFile(file.Path) || seen[file.Path] || file.Bytes < 0 {
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
	status, err := tree.Status()
	if err != nil {
		return "", err
	}
	for _, file := range status {
		if file.Staging != gogit.Unmodified && file.Staging != gogit.Untracked {
			return "", errors.New("worktree already has staged changes; reconcile first")
		}
	}
	for _, path := range paths {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		// All ignores Path in go-git v5; stage exact paths to exclude unreviewed edits.
		if err = tree.AddWithOptions(&gogit.AddOptions{Path: path}); err != nil {
			return "", err
		}
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	author := &object.Signature{Name: "Nixmox", Email: "nixmox@localhost", When: time.Now()}
	hash, err := tree.Commit(message, &gogit.CommitOptions{Author: author, Committer: author})
	if err != nil {
		return "", err
	}
	w.commit = domain.Commit(hash.String())
	return w.commit, nil
}

func (g *Engine) Push(ctx context.Context, repository domain.Repository, commit domain.Commit, branch string) (domain.Publication, error) {
	result := domain.Publication{RepositoryID: repository.ID, Commit: commit, RemoteBranch: branch, RemoteURL: repository.Remote.URL, CredentialRef: repository.Remote.CredentialRef, State: "unknown"}
	if !validCommit(commit) {
		return result, errors.New("publication requires pinned commit")
	}
	ref, err := branchName(branch)
	if err != nil {
		return result, err
	}
	r, err := g.open(ctx, repository.ID)
	if err != nil {
		return result, err
	}
	if _, err = r.CommitObject(plumbing.NewHash(string(commit))); err != nil {
		return result, err
	}
	remote, auth, cleanup, err := g.remote(ctx, repository, r.Storer)
	defer cleanup()
	if err != nil {
		return result, err
	}
	err = remote.PushContext(ctx, &gogit.PushOptions{
		RemoteName: "upstream", Auth: auth,
		RefSpecs: []config.RefSpec{config.RefSpec(string(commit) + ":" + ref.String())},
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return result, &domain.Error{Code: "PUBLICATION_UNKNOWN", Message: "Push outcome requires remote observation before retry"}
	}
	return g.ObservePublication(ctx, result)
}

func (g *Engine) ObservePublication(ctx context.Context, p domain.Publication) (domain.Publication, error) {
	ref, err := branchName(p.RemoteBranch)
	if err != nil {
		return p, err
	}
	repository := domain.Repository{Record: domain.Record{ID: p.RepositoryID}, Remote: domain.Remote{URL: p.RemoteURL, CredentialRef: p.CredentialRef}}
	// Remote observation must also work after loss of the rebuildable local cache.
	remote, auth, cleanup, err := g.remote(ctx, repository, memory.NewStorage())
	defer cleanup()
	if err != nil {
		return p, err
	}
	refs, err := remote.ListContext(ctx, &gogit.ListOptions{Auth: auth})
	if err != nil {
		return p, err
	}
	p.RemoteHead, p.State = nil, "unknown"
	for _, candidate := range refs {
		if candidate.Name() != ref || candidate.Type() != plumbing.HashReference {
			continue
		}
		head := domain.Commit(candidate.Hash().String())
		p.RemoteHead, p.State = &head, "diverged"
		if head == p.Commit {
			p.State = "pushed"
		}
		return p, nil
	}
	return p, plumbing.ErrReferenceNotFound
}
