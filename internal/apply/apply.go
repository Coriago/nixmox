// Package apply drives nixos-rebuild apply/rollback jobs and records their
// outcomes (git SHA + generation before/after) to the apply log.
package apply

import (
	"context"
	"fmt"
	"log"
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
	return &Manager{
		cfg:         cfg,
		git:         git,
		records:     records,
		runner:      runner,
		store:       store,
		nodeID:      nodeID,
		repoDir:     cfg.DataDirPath("git", "repo"),
		profilesDir: "/nix/var/nix/profiles",
	}
}

func (m *Manager) Apply(ctx context.Context) (jobs.Job, error) {
	sha := m.git.State().LastPulledSHA
	if sha == "" {
		return jobs.Job{}, fmt.Errorf("nothing pulled yet — pull the gitops repo first")
	}
	args := []string{"--flake", m.repoDir + "#" + m.nodeID}
	return m.startJob(ctx, "apply", args, sha)
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
	return m.startJob(ctx, "rollback", []string{"--rollback"}, sha)
}

// OnNewSHA is the gitops poller callback: auto-apply when enabled.
func (m *Manager) OnNewSHA(sha string) {
	log.Printf("gitops: update available (%s)", sha)
	if !m.cfg.AutoApply {
		return
	}
	if _, err := m.Apply(context.Background()); err != nil {
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
		GenAfter:  LatestGeneration(m.profilesDir),
		JobID:     j.ID,
	}
	if err := m.records.Record(m.nodeID, r); err != nil {
		log.Printf("applylog: %v", err)
	}
}

func (m *Manager) startJob(ctx context.Context, kind string, args []string, sha string) (jobs.Job, error) {
	now := time.Now().Truncate(time.Millisecond)
	j := jobs.Job{
		ID:        fmt.Sprintf("job-%d", now.UnixNano()),
		Kind:      kind,
		Host:      m.nodeID,
		Args:      args,
		Mode:      m.cfg.ApplyMode,
		State:     "pending",
		CreatedAt: now,
		UpdatedAt: now,
		GitSHA:    sha,
		GenBefore: LatestGeneration(m.profilesDir),
	}
	if err := m.store.Create(j); err != nil {
		return jobs.Job{}, err
	}
	j, _ = m.store.Get(j.ID)
	if err := m.runner.Start(ctx, j); err != nil {
		return jobs.Job{}, err
	}
	return j, nil
}
