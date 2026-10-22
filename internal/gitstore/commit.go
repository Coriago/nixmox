package gitstore

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nixmox/internal/domain"
)

type Increment struct {
	Commit, Path, Digest string
	Bytes                int64
	Prerequisites        []string
}

func (g *Git) Commit(ctx context.Context, id, base, remoteBase, owner string, files map[string][]byte, at time.Time) (Increment, error) {
	dir, err := g.Worktree(ctx, id, base, owner)
	if err != nil {
		return Increment{}, err
	}
	release, err := g.lock(ctx, id)
	if err != nil {
		return Increment{}, err
	}
	defer release()
	if len(files) == 0 || len(files) > 128 {
		return Increment{}, domain.Err("INVALID_PAYLOAD", "A source change requires 1–128 files.", 422)
	}
	args := []string{"add", "--"}
	for name, data := range files {
		if err = WriteFile(dir, name, data); err != nil {
			return Increment{}, err
		}
		args = append(args, name)
	}
	if _, err = g.run(ctx, dir, nil, args...); err != nil {
		return Increment{}, domain.Err("SOURCE_UNAVAILABLE", "Changes could not be staged.", 503)
	}
	tree, err := g.run(ctx, dir, nil, "write-tree")
	if err != nil {
		return Increment{}, err
	}
	when := at.UTC().Format(time.RFC3339)
	env := []string{"GIT_AUTHOR_NAME=Nixmox", "GIT_AUTHOR_EMAIL=nixmox@localhost", "GIT_COMMITTER_NAME=Nixmox", "GIT_COMMITTER_EMAIL=nixmox@localhost", "GIT_AUTHOR_DATE=" + when, "GIT_COMMITTER_DATE=" + when}
	r, err := g.run(ctx, dir, env, "commit-tree", strings.TrimSpace(string(tree.Stdout)), "-p", base, "-m", "Nixmox change "+owner)
	if err != nil {
		return Increment{}, domain.Err("SOURCE_UNAVAILABLE", "Git commit could not be created.", 503)
	}
	commit := strings.TrimSpace(string(r.Stdout))
	if !CommitPattern.MatchString(commit) {
		return Increment{}, domain.Err("SOURCE_UNAVAILABLE", "Git produced an invalid commit identity.", 503)
	}
	if _, err = g.run(ctx, g.Bare(id), nil, "merge-base", "--is-ancestor", remoteBase, commit); err != nil {
		return Increment{}, domain.Err("SOURCE_UNAVAILABLE", "The remote prerequisite is not an ancestor of the change.", 409)
	}
	ref := "refs/nixmox/artifacts/" + owner
	if _, err = g.run(ctx, g.Bare(id), nil, "update-ref", ref, commit); err != nil {
		return Increment{}, err
	}
	bundle := filepath.Join(g.Root, "increments", id, owner+".bundle")
	if err = os.MkdirAll(filepath.Dir(bundle), 0700); err != nil {
		return Increment{}, err
	}
	if _, err = g.run(ctx, g.Bare(id), nil, "bundle", "create", bundle, ref, "^"+remoteBase); err != nil {
		return Increment{}, domain.Err("SOURCE_UNAVAILABLE", "An incremental Git bundle could not be created.", 503)
	}
	f, err := os.Open(bundle)
	if err != nil {
		return Increment{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 64<<20 {
		return Increment{}, domain.Err("STORAGE_LIMIT", "Incremental bundle exceeds 64 MiB.", 507)
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return Increment{}, err
	}
	if _, err = f.Seek(0, 0); err != nil {
		return Increment{}, err
	}
	prerequisites := []string{}
	scan := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for scan.Scan() {
		line := scan.Text()
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "-") {
			fields := strings.Fields(line[1:])
			if len(fields) == 0 || !CommitPattern.MatchString(fields[0]) {
				return Increment{}, domain.Err("ARTIFACT_INVALID", "Malformed bundle prerequisite.", 409)
			}
			prerequisites = append(prerequisites, fields[0])
		}
	}
	if len(prerequisites) == 0 {
		return Increment{}, domain.Err("ARTIFACT_INVALID", "Full repository bundles cannot enter cluster storage.", 409)
	}
	if _, err = g.run(ctx, g.Bare(id), nil, "bundle", "verify", bundle); err != nil {
		return Increment{}, domain.Err("ARTIFACT_INVALID", "Git bundle verification failed.", 409)
	}
	return Increment{Commit: commit, Path: bundle, Digest: hex.EncodeToString(hash.Sum(nil)), Bytes: info.Size(), Prerequisites: prerequisites}, nil
}
func (g *Git) Import(ctx context.Context, id, path, commit string) error {
	if !CommitPattern.MatchString(commit) {
		return domain.Err("ARTIFACT_INVALID", "Invalid increment commit.", 409)
	}
	release, err := g.lock(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if _, err = g.run(ctx, g.Bare(id), nil, "bundle", "verify", path); err != nil {
		return domain.Err("SOURCE_UNAVAILABLE", "Bundle prerequisites are missing or the increment is invalid.", 503)
	}
	if _, err = g.run(ctx, g.Bare(id), nil, "bundle", "unbundle", path); err != nil {
		return domain.Err("ARTIFACT_INVALID", "Incremental objects failed verification.", 409)
	}
	if !g.HasCommit(ctx, id, commit) {
		return domain.Err("ARTIFACT_INVALID", "Increment does not contain its pinned commit.", 409)
	}
	return nil
}
