// Package gitstore owns bounded Git processes and private materialized sources.
package gitstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/process"
)

var CommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Git struct {
	Authorize func(context.Context, string, string) ([]string, func(), error)
	Root      string
	mu        sync.Mutex
	locks     map[string]chan struct{}
}

func New(root string) (*Git, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Git{Root: root, locks: map[string]chan struct{}{}}, nil
}
func StableID(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + hex.EncodeToString(sum[:16])
}
func (g *Git) lock(ctx context.Context, id string) (func(), error) {
	if !domain.SafeID.MatchString(id) {
		return nil, domain.Err("INVALID_ID", "Invalid repository ID.", 422)
	}
	g.mu.Lock()
	ch := g.locks[id]
	if ch == nil {
		if len(g.locks) >= 1000 {
			g.mu.Unlock()
			return nil, domain.Err("STORAGE_LIMIT", "Local repository capacity reached.", 507)
		}
		ch = make(chan struct{}, 1)
		g.locks[id] = ch
	}
	g.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (g *Git) Bare(id string) string { return filepath.Join(g.Root, "repos", id+".git") }
func (g *Git) run(ctx context.Context, dir string, extra []string, args ...string) (process.Result, error) {
	env := []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=file:http:https:ssh", "GIT_LFS_SKIP_SMUDGE=1", "GIT_SSH_COMMAND=ssh -F /dev/null -oBatchMode=yes -oStrictHostKeyChecking=yes -oIdentityAgent=none -oIdentitiesOnly=yes -oIdentityFile=none -oConnectTimeout=10"}
	env = append(env, extra...)
	env = append(env, "HOME="+filepath.Join(g.Root, "isolated-home"))
	args = append([]string{"-c", "core.fsync=all", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "http.followRedirects=false", "-c", "http.sslVerify=true", "-c", "protocol.ext.allow=never", "-c", "submodule.recurse=false", "-c", "fetch.fsckObjects=true", "-c", "transfer.fsckObjects=true"}, args...)
	return process.Run(ctx, process.Options{Directory: dir, Environment: env, Timeout: 2 * time.Minute, MaxOutput: 4 << 20}, "git", args...)
}
func (g *Git) ensure(ctx context.Context, id string) error {
	dir := g.Bare(id)
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return nil
	}
	_, err := g.run(ctx, "", nil, "init", "--bare", "--template=", dir)
	return err
}
func ValidateBranch(branch string) error {
	if branch == "" || len(branch) > 256 || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " ~^:?*[\\\x00\r\n") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") {
		return domain.Err("INVALID_PAYLOAD", "Invalid Git branch.", 422)
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return domain.Err("INVALID_PAYLOAD", "Invalid Git branch.", 422)
		}
	}
	return nil
}
func (g *Git) Fetch(ctx context.Context, id, remote, branch string, credential *string) (string, error) {
	if config.ValidateRemote(remote) != nil {
		return "", domain.Err("INVALID_PAYLOAD", "Invalid Git remote; use a credential reference for secrets.", 422)
	}
	if err := ValidateBranch(branch); err != nil {
		return "", err
	}
	release, err := g.lock(ctx, id)
	if err != nil {
		return "", err
	}
	defer release()
	if err = g.ensure(ctx, id); err != nil {
		return "", domain.Err("SOURCE_UNAVAILABLE", "Local Git storage could not be initialized.", 503)
	}
	ref := "refs/nixmox/fetched/" + StableID("branch_", branch)
	if _, err = g.remoteRun(ctx, g.Bare(id), remote, credential, "fetch", "--no-tags", "--no-recurse-submodules", remote, "+refs/heads/"+branch+":"+ref); err != nil {
		return "", domain.Err("SOURCE_UNAVAILABLE", "Git fetch failed; remote access or the selected branch is unavailable.", 503)
	}
	return g.resolve(ctx, id, ref)
}

func (g *Git) remoteRun(ctx context.Context, dir, remote string, credential *string, args ...string) (process.Result, error) {
	var env []string
	if credential != nil {
		if g.Authorize == nil {
			return process.Result{}, domain.Err("CREDENTIAL_UNAVAILABLE", "Managed Git credential is unavailable on this node.", 503)
		}
		values, cleanup, err := g.Authorize(ctx, *credential, remote)
		if err != nil {
			return process.Result{}, err
		}
		defer cleanup()
		env = values
	}
	return g.run(ctx, dir, env, args...)
}

func (g *Git) CheckRemote(ctx context.Context, remote, branch, credential string) error {
	if config.ValidateRemote(remote) != nil || ValidateBranch(branch) != nil {
		return domain.Err("INVALID_SOURCE", "Invalid repository remote.", 422)
	}
	r, err := g.remoteRun(ctx, "", remote, &credential, "ls-remote", "--exit-code", remote, "refs/heads/"+branch)
	if err != nil || len(r.Stdout) == 0 {
		return domain.Err("CREDENTIAL_UNAVAILABLE", "Repository read access could not be verified.", 503)
	}
	return nil
}
func (g *Git) resolve(ctx context.Context, id, ref string) (string, error) {
	r, err := g.run(ctx, g.Bare(id), nil, "rev-parse", "--verify", ref+"^{commit}")
	commit := strings.TrimSpace(string(r.Stdout))
	if err != nil || !CommitPattern.MatchString(commit) {
		return "", domain.Err("SOURCE_UNAVAILABLE", "The pinned Git commit is unavailable.", 503)
	}
	return commit, nil
}
func (g *Git) HasCommit(ctx context.Context, id, commit string) bool {
	if !CommitPattern.MatchString(commit) {
		return false
	}
	got, err := g.resolve(ctx, id, commit)
	return err == nil && got == commit
}
func SafePath(name string) bool {
	return name != "" && name != "." && !filepath.IsAbs(name) && filepath.ToSlash(filepath.Clean(name)) == name && !strings.HasPrefix(name, "../") && name != ".." && !strings.ContainsAny(name, "\\\x00\r\n") && !strings.HasPrefix(name, ".git/") && name != ".git"
}
func (g *Git) ReadFile(ctx context.Context, id, commit, name string) ([]byte, error) {
	return g.readFile(ctx, g.Bare(id), commit, name)
}
func ReadWorktreeFile(ctx context.Context, root, commit, name string) ([]byte, error) {
	return (&Git{}).readFile(ctx, root, commit, name)
}
func (g *Git) readFile(ctx context.Context, directory, commit, name string) ([]byte, error) {
	if !CommitPattern.MatchString(commit) || !SafePath(name) {
		return nil, domain.Err("INVALID_PATH", "Invalid pinned source path.", 422)
	}
	r, err := g.run(ctx, directory, nil, "ls-tree", commit, "--", name)
	if err != nil {
		return nil, domain.Err("SOURCE_UNAVAILABLE", "Source tree unavailable.", 503)
	}
	if len(r.Stdout) == 0 {
		return nil, domain.Err("NOT_FOUND", "Source file not found.", 404)
	}
	if !strings.HasPrefix(string(r.Stdout), "100644 blob ") && !strings.HasPrefix(string(r.Stdout), "100755 blob ") {
		return nil, domain.Err("UNSAFE_PATH", "Source file is not a regular file.", 422)
	}
	r, err = g.run(ctx, directory, nil, "show", commit+":"+name)
	if err != nil {
		return nil, domain.Err("SOURCE_UNAVAILABLE", "Source file is unavailable or exceeds its size bound.", 503)
	}
	return r.Stdout, nil
}
func (g *Git) Files(ctx context.Context, id, commit, prefix string) ([]string, error) {
	if !CommitPattern.MatchString(commit) || !SafePath(prefix) {
		return nil, domain.Err("INVALID_PATH", "Invalid source directory.", 422)
	}
	r, err := g.run(ctx, g.Bare(id), nil, "ls-tree", "-r", "--name-only", "-z", commit, "--", prefix)
	if err != nil {
		return nil, domain.Err("SOURCE_UNAVAILABLE", "Source manifest unavailable.", 503)
	}
	files := []string{}
	for _, name := range strings.Split(string(r.Stdout), "\x00") {
		if name != "" {
			if !SafePath(name) || len(files) >= 4096 {
				return nil, domain.Err("STORAGE_LIMIT", "Source manifest exceeds its bound.", 507)
			}
			files = append(files, name)
		}
	}
	return files, nil
}
func (g *Git) Worktree(ctx context.Context, id, commit, owner string) (string, error) {
	if !CommitPattern.MatchString(commit) || !domain.SafeID.MatchString(owner) {
		return "", domain.Err("INVALID_ID", "Invalid source checkout identity.", 422)
	}
	release, err := g.lock(ctx, id)
	if err != nil {
		return "", err
	}
	defer release()
	dir := filepath.Join(g.Root, "worktrees", id, owner)
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(dir, ".git")); err == nil {
		r, err := g.run(ctx, dir, nil, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(string(r.Stdout)) != commit {
			return "", domain.Err("SOURCE_CONFLICT", "Worktree no longer has its pinned base.", 409)
		}
		return dir, nil
	}
	_, err = g.run(ctx, g.Bare(id), nil, "worktree", "add", "--detach", dir, commit)
	if err != nil {
		return "", domain.Err("SOURCE_UNAVAILABLE", "Pinned source could not be materialized.", 503)
	}
	return dir, nil
}

// WriteFile rejects symlinks at every component, including in a checked-out tree.
func WriteFile(root, name string, data []byte) error {
	if !SafePath(name) || len(data) > 4<<20 {
		return domain.Err("INVALID_PATH", "Unsafe or oversized draft file.", 422)
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer base.Close()
	parts := strings.Split(name, "/")
	for i := 1; i <= len(parts); i++ {
		current := strings.Join(parts[:i], "/")
		info, err := base.Lstat(current)
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || (i < len(parts) && !info.IsDir())) {
			return domain.Err("UNSAFE_PATH", "Draft path traverses a symlink or non-directory.", 422)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if i < len(parts) && errors.Is(err, os.ErrNotExist) {
			if err = base.Mkdir(current, 0755); err != nil {
				return err
			}
		}
	}
	f, err := base.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
