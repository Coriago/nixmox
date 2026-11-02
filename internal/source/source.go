// Package source resolves tracked Git references and creates isolated pinned
// worktrees. Existing rendered worktrees are never pulled or reused.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"go.opentelemetry.io/otel"
)

type Repository struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	CredentialID string `json:"credentialId,omitempty"`
	Ref          string `json:"ref"`
	SHA          string `json:"sha"`
}

type Pin struct {
	ID  string `json:"id"`
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}
type Configuration struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	TargetRepo         Pin    `json:"targetRepo"`
	FlakeDir           string `json:"flakeDir"`
	FlakeConfiguration string `json:"flakeConfiguration"`
}

var commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (p Pin) Validate() error {
	if p.ID == "" || p.Ref == "" || !commitHash.MatchString(p.SHA) {
		return errors.New("repository ID, reference and full immutable commit SHA required")
	}
	return nil
}

type Manager struct {
	Root string
	// Auth obtains native go-git client options from the credential service.
	// SSH options must include verified host keys; insecure options are forbidden.
	Auth func(context.Context, Repository) ([]client.Option, error)
}

type Worktree struct {
	Path       string
	Repository *git.Repository
	Pin        Pin
}

func (w *Worktree) Close() error { return os.RemoveAll(w.Path) }

func (m Manager) clone(ctx context.Context, repo Repository) (*Worktree, error) {
	if strings.TrimSpace(repo.URL) == "" || repo.Ref == "" {
		return nil, errors.New("repository URL and reference required")
	}
	if m.Root == "" {
		return nil, errors.New("private worktree root required")
	}
	if endpoint, err := url.Parse(repo.URL); err == nil && endpoint.Scheme != "" {
		if endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.User != nil && endpoint.Scheme != "ssh") {
			return nil, errors.New("repository URLs must not embed credentials, queries, or fragments; use a credential reference")
		}
		if endpoint.User != nil {
			if _, password := endpoint.User.Password(); password {
				return nil, errors.New("SSH passwords must use a credential reference")
			}
		}
	}
	root, err := filepath.Abs(m.Root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path, err := os.MkdirTemp(root, "worktree-")
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(path)
		}
	}()
	var options []client.Option
	if repo.CredentialID != "" {
		if m.Auth == nil {
			return nil, errors.New("repository credentials unavailable")
		}
		options, err = m.Auth(ctx, repo)
		if err != nil {
			return nil, err
		}
	}
	reference := plumbing.ReferenceName(repo.Ref)
	if !strings.HasPrefix(repo.Ref, "refs/") {
		reference = plumbing.NewBranchReferenceName(repo.Ref)
	}
	repository, err := git.PlainCloneContext(ctx, path, &git.CloneOptions{URL: repo.URL, ReferenceName: reference, ClientOptions: options, NoCheckout: true})
	if err != nil {
		return nil, err
	}
	success = true
	return &Worktree{Path: path, Repository: repository}, nil
}

func (m Manager) Refresh(ctx context.Context, repo Repository) (Repository, error) {
	ctx, span := otel.Tracer("nixmox.source").Start(ctx, "source.refresh")
	defer span.End()
	work, err := m.clone(ctx, repo)
	if err != nil {
		return repo, err
	}
	defer work.Close()
	head, err := work.Repository.Head()
	if err != nil {
		return repo, err
	}
	repo.SHA = head.Hash().String()
	return repo, nil
}

func (m Manager) Checkout(ctx context.Context, repo Repository, pin Pin) (*Worktree, error) {
	ctx, span := otel.Tracer("nixmox.source").Start(ctx, "source.checkout")
	defer span.End()
	if err := pin.Validate(); err != nil {
		return nil, err
	}
	if pin.ID != repo.ID {
		return nil, errors.New("repository pin identity mismatch")
	}
	repo.Ref = pin.Ref
	work, err := m.clone(ctx, repo)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			work.Close()
		}
	}()
	hash := plumbing.NewHash(pin.SHA)
	if _, err = work.Repository.CommitObject(hash); err != nil {
		return nil, fmt.Errorf("pinned commit unavailable: %w", err)
	}
	tree, err := work.Repository.Worktree()
	if err != nil {
		return nil, err
	}
	if err = tree.Checkout(&git.CheckoutOptions{Hash: hash}); err != nil {
		return nil, err
	}
	work.Pin = pin
	success = true
	return work, nil
}

// ReadFile uses os.Root so repository symlinks cannot escape the worktree.
func (w *Worktree) ReadFile(path string) ([]byte, error) {
	if !filepath.IsLocal(path) {
		return nil, errors.New("repository path must be relative")
	}
	root, err := os.OpenRoot(w.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, errors.New("repository file exceeds 8 MiB")
	}
	return data, nil
}

// Directory resolves a working directory without permitting repository symlinks
// to redirect command execution outside the pinned worktree.
func (w *Worktree) Directory(path string) (string, error) {
	if !filepath.IsLocal(path) {
		return "", errors.New("repository directory must be relative")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(w.Path, path))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(w.Path, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("repository directory escapes worktree")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("repository directory is not a directory")
	}
	return resolved, nil
}
