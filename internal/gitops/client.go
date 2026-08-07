// Package gitops manages the local clone of the declarative NixOS config repo:
// clone/fetch/pull via the git CLI, last-pulled SHA tracking, and update detection.
package gitops

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Client abstracts git operations so the poller and manager can be tested
// without invoking real git processes.
type Client interface {
	Clone(ctx context.Context, url, branch, dir string) error
	Fetch(ctx context.Context, dir string) error
	RemoteHEAD(ctx context.Context, dir, branch string) (string, error)
	ResetHard(ctx context.Context, dir, branch string) error
	CurrentSHA(ctx context.Context, dir string) (string, error)
}

// ExecClient implements Client by shelling out to the git binary.
// ExtraEnv is appended to each command's environment (e.g. GIT_SSH_COMMAND).
type ExecClient struct {
	ExtraEnv []string
}

func (c ExecClient) run(ctx context.Context, dir string, args ...string) (string, error) {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	if len(c.ExtraEnv) > 0 {
		cmd.Env = append(cmd.Environ(), c.ExtraEnv...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (c ExecClient) Clone(ctx context.Context, url, branch, dir string) error {
	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, url, dir)
	_, err := c.run(ctx, "", args...)
	return err
}

func (c ExecClient) Fetch(ctx context.Context, dir string) error {
	_, err := c.run(ctx, dir, "fetch", "origin")
	return err
}

func (c ExecClient) RemoteHEAD(ctx context.Context, dir, branch string) (string, error) {
	return c.run(ctx, dir, "rev-parse", "origin/"+branch)
}

func (c ExecClient) ResetHard(ctx context.Context, dir, branch string) error {
	_, err := c.run(ctx, dir, "reset", "--hard", "origin/"+branch)
	return err
}

func (c ExecClient) CurrentSHA(ctx context.Context, dir string) (string, error) {
	return c.run(ctx, dir, "rev-parse", "HEAD")
}
