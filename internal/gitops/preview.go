package gitops

import (
	"context"
	"fmt"
	"regexp"
)

type Preview struct {
	Base    string `json:"base"`
	Target  string `json:"target"`
	Commits string `json:"commits"`
	Files   string `json:"files"`
	Summary string `json:"summary"`
}

var revision = regexp.MustCompile(`^[a-fA-F0-9]{4,64}$`)

func (m *Manager) Preview(ctx context.Context, base string) (Preview, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	target := m.state.LastPulledSHA
	if m.state.UpdateAvailable && m.state.RemoteSHA != "" {
		target = m.state.RemoteSHA
	}
	if base == "" {
		base = m.state.LastPulledSHA
	}
	p := Preview{Base: base, Target: target}
	if !revision.MatchString(base) || !revision.MatchString(target) {
		return p, fmt.Errorf("pull the repository before previewing changes")
	}
	c, ok := m.client.(interface {
		Changes(context.Context, string, string, string) (Preview, error)
	})
	if !ok {
		return p, fmt.Errorf("git client cannot preview changes")
	}
	return c.Changes(ctx, m.dir, base, target)
}
func (c ExecClient) Changes(ctx context.Context, dir, base, target string) (Preview, error) {
	p := Preview{Base: base, Target: target}
	var err error
	p.Commits, err = c.run(ctx, dir, "log", "--max-count=100", "--oneline", base+".."+target, "--")
	if err != nil {
		return p, err
	}
	p.Files, err = c.run(ctx, dir, "diff", "--name-status", base, target, "--")
	if err != nil {
		return p, err
	}
	p.Summary, err = c.run(ctx, dir, "diff", "--stat", base, target, "--")
	return p, err
}
