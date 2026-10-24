// Package git implements pinned Git sources, isolated checkouts and explicit publication.
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

	"github.com/go-git/go-billy/v6/osfs"
	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"
	gitssh "github.com/go-git/go-git/v6/plumbing/transport/ssh"
	"github.com/go-git/go-git/v6/storage"
	"github.com/go-git/go-git/v6/storage/memory"
	linked "github.com/go-git/go-git/v6/x/plumbing/worktree"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

var _ engine.Git = (*Engine)(nil)

type Engine struct {
	Root string
	// Credentials returns holder-owned in-process authentication.
	// SSH requires PublicKeys with an explicit host-key verifier; never an ambient agent.
	Credentials func(context.Context, domain.Repository) (Authentication, error)
}

// Authentication keeps managed HTTP credentials and verified SSH keys explicit.
type Authentication struct {
	HTTP client.HTTPAuth
	SSH  *gitssh.PublicKeys
}

func (a Authentication) options() []client.Option {
	var options []client.Option
	if a.HTTP != nil {
		options = append(options, client.WithHTTPAuth(a.HTTP))
	}
	if a.SSH != nil {
		options = append(options, client.WithSSHAuth(a.SSH))
	}
	return options
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

func (g *Engine) remote(ctx context.Context, repository domain.Repository, store storage.Storer) (*gogit.Remote, Authentication, error) {
	if err := ctx.Err(); err != nil {
		return nil, Authentication{}, err
	}
	if !validRemote(repository.Remote.URL) {
		return nil, Authentication{}, errors.New("remote requires HTTPS, SSH or absolute file URL without embedded secrets")
	}
	var auth Authentication
	if repository.Remote.CredentialRef != nil {
		if g.Credentials == nil {
			return nil, Authentication{}, &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "Git credential holder unavailable"}
		}
		value, err := g.Credentials(ctx, repository)
		if err != nil {
			return nil, Authentication{}, err
		}
		if value.HTTP == nil && value.SSH == nil {
			return nil, Authentication{}, &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "Git credential holder returned no authentication"}
		}
		auth = value
	}
	if strings.HasPrefix(repository.Remote.URL, "ssh:") {
		keys := auth.SSH
		if keys == nil || keys.Signer == nil || keys.HostKeyCallback == nil {
			return nil, Authentication{}, &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "SSH requires a managed key and explicit host-key verification"}
		}
	}
	return gogit.NewRemote(store, &config.RemoteConfig{Name: "upstream", URLs: []string{repository.Remote.URL}}), auth, nil
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
	r, err := g.open(ctx, id)
	if err == nil {
		return r.Close()
	}
	if !errors.Is(err, gogit.ErrRepositoryNotExists) {
		return err
	}
	dir := g.directory(id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	r, err = gogit.PlainInit(dir, true)
	if err != nil {
		return err
	}
	return r.Close()
}

func branchName(branch string) (plumbing.ReferenceName, error) {
	if strings.HasPrefix(branch, "-") {
		return "", errors.New("branch name must not start with a dash")
	}
	ref := plumbing.NewBranchReferenceName(branch)
	return ref, ref.Validate()
}

// ValidCommit validates the exact object ID supported by this adapter.
func ValidCommit(commit domain.Commit) bool { return validCommit(commit) }

// This adapter currently accepts SHA-1 repositories only. SHA-256 support needs
// explicit format handling, never truncation of an object ID.
func validCommit(commit domain.Commit) bool {
	return len(commit) == 40 && shared.ValidCommit(commit)
}

// RemoteHead observes upstream without fetching or accepting a new source.
func (g *Engine) RemoteHead(ctx context.Context, repository domain.Repository) (domain.Commit, error) {
	branch, err := branchName(repository.Upstream.Branch)
	if err != nil {
		return "", err
	}
	remote, auth, err := g.remote(ctx, repository, memory.NewStorage())
	if err != nil {
		return "", err
	}
	refs, err := remote.ListContext(ctx, &gogit.ListOptions{ClientOptions: auth.options()})
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		if ref.Name() == branch && ref.Type() == plumbing.HashReference {
			return domain.Commit(ref.Hash().String()), nil
		}
	}
	return "", plumbing.ErrReferenceNotFound
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
	defer r.Close()
	remote, auth, err := g.remote(ctx, repository, r.Storer)
	if err != nil {
		return "", err
	}
	ref := plumbing.ReferenceName("refs/remotes/upstream/" + repository.Upstream.Branch)
	err = remote.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: "upstream", ClientOptions: auth.options(), Tags: gogit.NoTags,
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
	repository *gogit.Repository
	worktrees  *linked.Worktree
	engine     *Engine
}

func (w *gitWorktree) Path() string { return w.path }

func (w *gitWorktree) Commit() domain.Commit { return w.commit }

func (w *gitWorktree) Close() error {
	return errors.Join(w.worktrees.Remove(filepath.Base(w.path)), w.repository.Close(), os.RemoveAll(w.path))
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
	keepOpen := false
	defer func() {
		if !keepOpen {
			r.Close()
		}
	}()
	if _, err := r.CommitObject(plumbing.NewHash(string(source.Commit))); err != nil {
		return nil, &domain.Error{Code: "SOURCE_UNAVAILABLE", Message: "Pinned Git source is not materialized"}
	}
	worktrees := filepath.Join(g.Root, "worktrees")
	if err := os.MkdirAll(worktrees, 0700); err != nil {
		return nil, err
	}
	path, err := os.MkdirTemp(worktrees, "draft-")
	if err != nil {
		return nil, err
	}
	manager, err := linked.New(r.Storer)
	if err != nil {
		os.RemoveAll(path)
		return nil, err
	}
	err = manager.Add(osfs.New(path, osfs.WithBoundOS()), filepath.Base(path), linked.WithCommit(plumbing.NewHash(string(source.Commit))), linked.WithDetachedHead())
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		manager.Remove(filepath.Base(path))
		os.RemoveAll(path)
		return nil, err
	}
	keepOpen = true
	return &gitWorktree{path: path, commit: source.Commit, repository: r, worktrees: manager, engine: g}, nil
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
	r, err := gogit.PlainOpen(w.Path())
	if err != nil {
		return "", err
	}
	defer r.Close()
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
		// Stage exact paths to exclude unreviewed edits.
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
	defer r.Close()
	if _, err = r.CommitObject(plumbing.NewHash(string(commit))); err != nil {
		return result, err
	}
	remote, auth, err := g.remote(ctx, repository, r.Storer)
	if err != nil {
		return result, err
	}
	err = remote.PushContext(ctx, &gogit.PushOptions{
		RemoteName: "upstream", ClientOptions: auth.options(),
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
	remote, auth, err := g.remote(ctx, repository, memory.NewStorage())
	if err != nil {
		return p, err
	}
	refs, err := remote.ListContext(ctx, &gogit.ListOptions{ClientOptions: auth.options()})
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
