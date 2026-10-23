package git

// go-git v5.19.3 does not implement bundles, rebase or linked-worktree
// creation/removal (see its COMPATIBILITY.md). Keep those operations on Git;
// do not duplicate its pack format, merge machinery or worktree administration.
import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
	"nixmox/internal/engine/task"
)

func (g *Engine) plan(directory string, args ...string) engine.CommandLine {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "protocol.allow=never", "-c", "protocol.file.allow=always", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always", "-c", "core.fsync=committed", "-c", "core.fsyncMethod=fsync", "-c", "transfer.fsckObjects=true"}
	if directory != "" {
		base = append(base, "-C", directory)
	}
	return engine.CommandLine{Program: "git", Args: append(base, args...), Environment: map[string]string{"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0", "GIT_SSH_COMMAND": "ssh -F /dev/null -oBatchMode=yes -oIdentityAgent=none -oIdentitiesOnly=yes", "GIT_AUTHOR_NAME": "Nixmox", "GIT_AUTHOR_EMAIL": "nixmox@localhost", "GIT_COMMITTER_NAME": "Nixmox", "GIT_COMMITTER_EMAIL": "nixmox@localhost"}}
}

func (g *Engine) run(ctx context.Context, directory string, args ...string) ([]byte, error) {
	if g.Runner == nil {
		return nil, errors.New("Git runner required for bundles, rebase and linked worktrees")
	}
	return task.Capture(ctx, g.Runner, g.plan(directory, args...), domain.QueryTotalLimit)
}

func (g *Engine) Bundle(ctx context.Context, repository domain.Repository, commit domain.Commit, prerequisites []domain.Commit, writer io.Writer) (domain.Bundle, error) {
	if !validCommit(commit) || len(prerequisites) == 0 || len(prerequisites) > 128 {
		return domain.Bundle{}, errors.New("incremental bundle requires pinned prerequisites")
	}
	r, err := g.open(ctx, repository.ID)
	if err != nil {
		return domain.Bundle{}, err
	}
	head, err := r.CommitObject(plumbing.NewHash(string(commit)))
	if err != nil {
		return domain.Bundle{}, err
	}
	for _, base := range prerequisites {
		if !validCommit(base) || base == commit {
			return domain.Bundle{}, errors.New("invalid bundle prerequisite")
		}
		if err = ctx.Err(); err != nil {
			return domain.Bundle{}, err
		}
		parent, err := r.CommitObject(plumbing.NewHash(string(base)))
		if err != nil {
			return domain.Bundle{}, err
		}
		if ancestor, err := parent.IsAncestor(head); err != nil || !ancestor {
			return domain.Bundle{}, errors.New("bundle prerequisite is not an available ancestor")
		}
	}
	temp, err := os.MkdirTemp(g.Root, "bundle-")
	if err != nil {
		return domain.Bundle{}, err
	}
	defer os.RemoveAll(temp)
	ref := plumbing.ReferenceName("refs/nixmox/bundles/" + filepath.Base(temp))
	if err = r.Storer.SetReference(plumbing.NewHashReference(ref, head.Hash)); err != nil {
		return domain.Bundle{}, err
	}
	defer r.Storer.RemoveReference(ref)
	filename := filepath.Join(temp, "increment.bundle")
	args := []string{"bundle", "create", filename, ref.String()}
	for _, base := range prerequisites {
		args = append(args, "^"+string(base))
	}
	if _, err = g.run(ctx, g.directory(repository.ID), args...); err != nil {
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
	if !ok || w.engine != g || !validCommit(onto) {
		return nil, errors.New("owned worktree and pinned rebase target required")
	}
	_, err := g.run(ctx, w.Path(), "rebase", string(onto))
	if err == nil {
		r, readErr := gogit.PlainOpenWithOptions(w.Path(), &gogit.PlainOpenOptions{EnableDotGitCommonDir: true})
		if readErr != nil {
			return nil, readErr
		}
		head, readErr := r.Head()
		if readErr != nil {
			return nil, readErr
		}
		w.commit = domain.Commit(head.Hash().String())
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
