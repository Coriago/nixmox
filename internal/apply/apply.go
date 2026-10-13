// Package apply drives nixos-rebuild apply/rollback jobs and records their
// outcomes (git SHA + generation before/after) to the apply log.
package apply

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"nixmox/internal/cluster"
	"nixmox/internal/config"
	"nixmox/internal/gitops"
	"nixmox/internal/jobs"
)

// LatestGeneration returns the highest NixOS generation number found in the
// system profiles dir (system-N-link entries). Using the latest generation
// rather than /run/current-system keeps the signal mode-agnostic: "boot"
// creates a generation without switching, "switch" does both.
// 0 means "unknown" (non-NixOS host).
func LatestGeneration(profilesDir string) int {
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return 0
	}
	max := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "system-") || !strings.HasSuffix(name, "-link") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "system-"), "-link"))
		if err == nil && n > max {
			max = n
		}
	}
	return max
}

type Manager struct {
	cfg         *config.Config
	git         *gitops.Manager
	records     cluster.RecordStore
	runner      *jobs.Runner
	store       jobs.Store
	nodeID      string
	repoDir     string
	profilesDir string
}

func NewManager(cfg *config.Config, git *gitops.Manager, records cluster.RecordStore, runner *jobs.Runner, store jobs.Store, nodeID string) *Manager {
	m := &Manager{
		cfg:         cfg,
		git:         git,
		records:     records,
		runner:      runner,
		store:       store,
		nodeID:      nodeID,
		repoDir:     cfg.DataDirPath("git", "repo"),
		profilesDir: "/nix/var/nix/profiles",
	}
	for _, kind := range []string{"apply", "rollback", "dry-build"} {
		runner.Register(kind, m.execute)
	}
	return m
}

func (m *Manager) execute(ctx context.Context, j *jobs.Job, out io.Writer) error {
	if j.Kind != "rollback" && j.GitSHA == "" {
		return fmt.Errorf("nothing pulled yet — pull the gitops repo first")
	}
	j.GenBefore = LatestGeneration(m.profilesDir)
	if err := m.store.Update(*j); err != nil {
		return err
	}
	mode := j.Mode
	if j.Kind == "dry-build" {
		mode = "dry-build"
	}
	err := jobs.RunCommand(ctx, j, out, "nixos-rebuild", append([]string{mode}, j.Args...)...)
	j.GenAfter = LatestGeneration(m.profilesDir)
	return err
}

func (m *Manager) Apply(ctx context.Context) (jobs.Job, error) {
	return m.build(ctx, "apply")
}

func (m *Manager) DryBuild(ctx context.Context) (jobs.Job, error) {
	return m.build(ctx, "dry-build")
}

func (m *Manager) build(ctx context.Context, kind string) (jobs.Job, error) {
	sha := m.git.State().LastPulledSHA
	target := m.cfg.FlakeAttribute
	if target == "" {
		target = m.nodeID
	}
	query := url.Values{"rev": {sha}}
	if m.cfg.FlakeDirectory != "" {
		query.Set("dir", m.cfg.FlakeDirectory)
	}
	ref := url.URL{Scheme: "git+file", Path: m.repoDir, RawQuery: query.Encode(), Fragment: target}
	args := []string{"--impure", "--no-reexec", "--flake", ref.String()}
	return m.startJob(ctx, kind, args, sha)
}

func (m *Manager) Rollback(ctx context.Context) (jobs.Job, error) {
	latest, ok, err := m.records.Latest(m.nodeID)
	if err != nil {
		return jobs.Job{}, err
	}
	sha := ""
	if ok {
		sha = latest.GitSHA
	}
	// Rollback uses existing generations; do not evaluate an unrelated
	// /etc/nixos configuration merely to rebuild nixos-rebuild itself.
	return m.startJob(ctx, "rollback", []string{"--rollback", "--no-reexec"}, sha)
}

// OnNewSHA is the gitops poller callback: auto-apply when enabled.
func (m *Manager) OnNewSHA(sha string) { m.OnNewSHAContext(context.Background(), sha) }

func (m *Manager) OnNewSHAContext(ctx context.Context, sha string) {
	log.Printf("gitops: update available (%s)", sha)
	if !m.cfg.AutoApply {
		return
	}
	if _, err := m.git.Ensure(ctx); err != nil {
		log.Printf("auto-apply pull: %v", err)
		return
	}
	if _, err := m.Apply(ctx); err != nil {
		log.Printf("auto-apply: %v", err)
	}
}

// HandleTerminal is the runner.OnTerminal hook: captures genAfter and writes
// the apply record for apply/rollback jobs.
func (m *Manager) HandleTerminal(j jobs.Job) {
	if j.Kind != "apply" && j.Kind != "rollback" {
		return
	}
	status := "success"
	if j.State != "applied" {
		status = "failed"
	}
	r := cluster.Record{
		Timestamp: time.Now(),
		Status:    status,
		GitSHA:    j.GitSHA,
		GenBefore: j.GenBefore,
		GenAfter:  j.GenAfter,
		JobID:     j.ID,
	}
	if err := m.records.Record(m.nodeID, r); err != nil {
		log.Printf("applylog: %v", err)
	}
}

func (m *Manager) startJob(ctx context.Context, kind string, args []string, sha string) (jobs.Job, error) {
	return m.runner.Submit(jobs.Job{
		Kind: kind, Host: m.nodeID, Args: args, Mode: m.cfg.ApplyMode,
		GitSHA: sha, GenBefore: LatestGeneration(m.profilesDir),
	})
}
