package gitstore

import (
	"context"
	"nixmox/internal/config"
	"nixmox/internal/domain"
	"strconv"
	"strings"
)

func (g *Git) FetchCommit(ctx context.Context, id, remote, commit string, credential *string) error {
	if config.ValidateRemote(remote) != nil {
		return domain.Err("INVALID_SOURCE", "Invalid remote prerequisite URL.", 422)
	}
	if !CommitPattern.MatchString(commit) {
		return domain.Err("INVALID_SOURCE", "Invalid pinned commit.", 422)
	}
	if g.HasCommit(ctx, id, commit) {
		return nil
	}
	release, err := g.lock(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if err = g.ensure(ctx, id); err != nil {
		return err
	}
	if _, err = g.remoteRun(ctx, g.Bare(id), remote, credential, "fetch", "--no-tags", "--no-recurse-submodules", remote, commit); err != nil {
		return domain.Err("SOURCE_UNAVAILABLE", "The remote no longer supplies the pinned prerequisite.", 503)
	}
	if !g.HasCommit(ctx, id, commit) {
		return domain.Err("SOURCE_UNAVAILABLE", "Pinned prerequisite is missing after fetch.", 503)
	}
	return nil
}
func (g *Git) Diff(ctx context.Context, id, from, to string) (string, error) {
	if !CommitPattern.MatchString(from) || !CommitPattern.MatchString(to) {
		return "", domain.Err("INVALID_SOURCE", "Diff requires exact commits.", 422)
	}
	r, err := g.run(ctx, g.Bare(id), nil, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", from, to, "--")
	if err != nil {
		return "", domain.Err("SOURCE_UNAVAILABLE", "Pinned diff is unavailable or exceeds its bound.", 503)
	}
	return string(r.Stdout), nil
}
func (g *Git) Counts(ctx context.Context, id, upstream, head string) (int, int, error) {
	if !CommitPattern.MatchString(upstream) || !CommitPattern.MatchString(head) {
		return 0, 0, domain.Err("INVALID_SOURCE", "Invalid ref commit.", 422)
	}
	r, err := g.run(ctx, g.Bare(id), nil, "rev-list", "--left-right", "--count", upstream+"..."+head)
	fields := strings.Fields(string(r.Stdout))
	if err != nil || len(fields) != 2 {
		return 0, 0, domain.Err("SOURCE_UNAVAILABLE", "Reference ancestry unavailable.", 503)
	}
	behind, e1 := strconv.Atoi(fields[0])
	ahead, e2 := strconv.Atoi(fields[1])
	if e1 != nil || e2 != nil {
		return 0, 0, domain.Err("SOURCE_UNAVAILABLE", "Invalid ancestry result.", 503)
	}
	return ahead, behind, nil
}
