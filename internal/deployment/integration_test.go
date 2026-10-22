package deployment_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"nixmox/internal/cluster"
	"nixmox/internal/config"
	"nixmox/internal/configuration"
	"nixmox/internal/deployment"
	"nixmox/internal/domain"
	"nixmox/internal/gitops"
	"nixmox/internal/gitstore"
	"nixmox/internal/host"
	"nixmox/internal/network"
	"nixmox/internal/nixstore"
	"nixmox/internal/service"
	"nixmox/internal/state"
	"nixmox/internal/storage"
)

func TestRepositoryAssignmentToPinnedCandidate(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("real Nix integration requires dev shell")
	}
	if _, err := os.Stat("/nix/var/nix/daemon-socket/socket"); err != nil {
		t.Skip("real Nix integration requires daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	packages, err := exec.CommandContext(ctx, "nix", "eval", "--raw", "--impure", "--expr", "(builtins.getFlake "+quote(root)+").inputs.nixpkgs.outPath").Output()
	if err != nil {
		t.Fatal(err)
	}
	system := runtime.GOARCH + "-linux"
	if runtime.GOARCH == "amd64" {
		system = "x86_64-linux"
	}
	if runtime.GOARCH == "arm64" {
		system = "aarch64-linux"
	}
	remote := t.TempDir()
	flake := `{ inputs.nixpkgs.url = ` + quote("path:"+strings.TrimSpace(string(packages))) + `; outputs = { self, nixpkgs }: let pkgs = import nixpkgs { system = ` + quote(system) + `; }; in { nixosConfigurations.fixture = { inherit pkgs; config.system.build.toplevel = pkgs.runCommand "nixmox-workflow-fixture" {} ''mkdir -p "$out/bin"; printf '#!${pkgs.runtimeShell}\nexit 0\n' > "$out/bin/switch-to-configuration"; chmod 755 "$out/bin/switch-to-configuration"''; }; }; }`
	if err = os.WriteFile(filepath.Join(remote, "flake.nix"), []byte(flake), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(remote, ".nixmox"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(remote, ".nixmox/configurations.json"), []byte(`{"schema_version":1,"configurations":[{"name":"fixture","flake_directory":".","flake_attribute":"nixosConfigurations.fixture"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.CommandContext(ctx, "nix", "flake", "lock", "path:"+remote).CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = remote
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	git("init", "-b", "main")
	git("add", ".")
	git("commit", "-m", "fixture")
	local, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	cfg := config.Default()
	cfg.DataDir = local.Root
	cfg.NATS.ClientPort = -1
	cfg.NATS.RoutePort = -1
	cfg.NATS.LeafPort = -1
	manager, err := cluster.Open(local, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	g, err := gitstore.New(local.Root + "/git")
	if err != nil {
		t.Fatal(err)
	}
	sources := &gitops.Service{State: state.Store{Acquire: manager.DurableStores}, Git: g}
	configurations, err := configuration.New(sources, local)
	if err != nil {
		t.Fatal(err)
	}
	sample, err := (host.Inspector{Config: cfg, BrokerReady: manager.BrokerReady}).Inspect(ctx, manager.Identity().NodeID)
	if err != nil {
		t.Fatal(err)
	}
	systemState := &fixtureSystem{}
	sample.Capabilities.Apply = true
	hosts := &host.Catalog{State: sources.State, Identity: manager.Identity, Members: manager.Members, Configurations: configurations, Sample: func() domain.HostSample {
		v := sample
		systemState.mu.Lock()
		defer systemState.mu.Unlock()
		if systemState.running != "" {
			running := systemState.running
			v.RunningOutput = &running
		}
		return v
	}}
	builds := &deployment.Service{Sources: sources, Configurations: configurations, Hosts: hosts, Identity: manager.Identity, Root: local.Root + "/builds", System: systemState, Nix: nixstore.Nix{Substituters: []string{"https://cache.nixos.org"}}}
	ops := append(manager.Operations(), sources.Operations()...)
	ops = append(ops, hosts.Operations()...)
	ops = append(ops, builds.Operations()...)
	svc, err := service.Open(local, network.Inspector{Config: cfg}, ops...)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err = manager.Attach(svc); err != nil {
		t.Fatal(err)
	}
	submit := func(id, kind string, p any, versions map[string]int64) domain.Task {
		t.Helper()
		b, _ := json.Marshal(p)
		receipt, err := svc.Admit(domain.Command{ID: id, Type: kind, TargetID: svc.Identity().NodeID, Payload: b, ExpectedVersions: versions})
		if err != nil {
			t.Fatal(kind, err)
		}
		for ctx.Err() == nil {
			task, err := svc.Task(receipt.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Terminal() || task.State == "blocked" {
				if task.State != "succeeded" {
					t.Fatalf("%s: %+v", kind, task)
				}
				return task
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(ctx.Err())
		return domain.Task{}
	}
	submit("cmd_create", "cluster.create", map[string]string{"name": "build-lab"}, nil)
	submit("cmd_import", "repository.import", map[string]string{"name": "lab", "remote_url": remote, "upstream_branch": "main"}, nil)
	repo, err := sources.Repository(ctx, gitstore.StableID("r_", "lab"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := configurations.Configurations(ctx, repo.ID, "", "", "100", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	configuration := page.Items[0]
	target, err := hosts.Host(ctx, svc.Identity().NodeID)
	if err != nil {
		t.Fatal(err)
	}
	submit("cmd_assign", "host.assign", map[string]string{"host_id": target.ID, "repository_id": repo.ID, "configuration": configuration.ID}, map[string]int64{"host:" + target.ID: target.Version, "configuration:" + configuration.ID: configuration.Version})
	assigned, err := hosts.Host(ctx, target.ID)
	if err != nil || assigned.Configuration == nil || assigned.Version <= target.Version {
		t.Fatal(assigned, err)
	}
	task := submit("cmd_build", "deployment.build", map[string]string{"host_id": target.ID}, map[string]int64{"host:" + target.ID: assigned.Version})
	var candidate domain.Candidate
	if json.Unmarshal(task.Result, &candidate) != nil {
		t.Fatal("invalid build result")
	}
	if candidate.Source.Commit != *repo.Upstream.Commit || candidate.BuilderID != target.ID || candidate.NarHash == "" || task.Progress == nil {
		t.Fatal("build lost pinned evidence", candidate, task)
	}
	stored, err := builds.Candidate(ctx, candidate.ID)
	if err != nil || stored.OutputPath != candidate.OutputPath || stored.NarHash != candidate.NarHash {
		t.Fatal("candidate projection differs", stored, err)
	}
	replay := submit("cmd_build", "deployment.build", map[string]string{"host_id": target.ID}, map[string]int64{"host:" + target.ID: assigned.Version})
	if replay.ID != task.ID {
		t.Fatal("build retry identity changed")
	}

	// Real immutable Nix closures, with activation effects isolated behind a
	// fixture target. No profile or running system on the developer is changed.
	baseline := func(label string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS(candidate.OutputPath)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "baseline"), []byte(label), 0600); err != nil {
			t.Fatal(err)
		}
		b, err := exec.CommandContext(ctx, "nix-store", "--add", dir).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	first, second := baseline("first"), baseline("second")
	systemState.set(first, 1)
	admit := func(id, kind string, p any, versions map[string]int64) domain.Receipt {
		t.Helper()
		b, _ := json.Marshal(p)
		r, err := svc.Admit(domain.Command{ID: id, Type: kind, TargetID: target.ID, Payload: b, ExpectedVersions: versions})
		if err != nil {
			t.Fatal(kind, err)
		}
		return r
	}
	await := func(id string, predicate func(domain.Task) bool) domain.Task {
		t.Helper()
		for ctx.Err() == nil {
			v, err := svc.Task(id)
			if err != nil {
				t.Fatal(err)
			}
			if predicate(v) {
				return v
			}
			if v.State == "failed" {
				t.Fatal(v.Error)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(ctx.Err())
		return domain.Task{}
	}
	versions := map[string]int64{"host:" + target.ID: assigned.Version}
	receipt := admit("cmd_apply_review", "deployment.apply", map[string]any{"candidate_id": candidate.ID}, versions)
	review := await(receipt.TaskID, func(v domain.Task) bool { return v.State == "blocked" && v.Phase == "review" })
	if review.Approval == nil || review.Approval.State != "pending" || len(review.Exclusions) != 0 || systemState.calls() != 0 {
		t.Fatal("review retained target lock or activated", review)
	}
	diff, err := builds.Diff(ctx, review.Approval.DiffID)
	if err != nil || diff.Baseline.OutputPath != first || diff.CandidateOutput != candidate.OutputPath {
		t.Fatal(diff, err)
	}
	changes, err := builds.DiffChanges(ctx, diff.ID, "", "", "1", "")
	if err != nil || len(changes.Items) != 1 {
		t.Fatal(changes, err)
	}
	if changes.NextCursor != nil {
		if _, err = builds.DiffChanges(ctx, diff.ID, "", "different filter", "1", *changes.NextCursor); err == nil {
			t.Fatal("cursor accepted different filter")
		}
	}
	// A running-generation change after review must invalidate approval.
	systemState.set(second, 2)
	submit("cmd_approve_stale", "deployment.approve", map[string]any{"task_id": review.ID, "diff_id": review.Approval.DiffID, "diff_version": review.Approval.DiffVersion}, nil)
	replacement := await(review.ID, func(v domain.Task) bool {
		return v.State == "blocked" && v.Phase == "review" && v.Approval != nil && v.Approval.DiffID != diff.ID
	})
	if len(replacement.Exclusions) != 0 || systemState.calls() != 0 {
		t.Fatal("stale approval activated or held target", replacement)
	}
	systemState.mu.Lock()
	systemState.lostReply = true
	systemState.mu.Unlock()
	submit("cmd_approve_current", "deployment.approve", map[string]any{"task_id": review.ID, "diff_id": replacement.Approval.DiffID, "diff_version": replacement.Approval.DiffVersion}, nil)
	done := await(review.ID, func(v domain.Task) bool { return v.State == "succeeded" })
	var deployed domain.Deployment
	if json.Unmarshal(done.Result, &deployed) != nil || deployed.Observation != "verified" || systemState.calls() != 1 {
		t.Fatal(deployed, done)
	}
	// The exact approval command replays its receipt, not target effects.
	submit("cmd_approve_current", "deployment.approve", map[string]any{"task_id": review.ID, "diff_id": replacement.Approval.DiffID, "diff_version": replacement.Approval.DiffVersion}, nil)
	if systemState.calls() != 1 {
		t.Fatal("activation repeated after lost reply")
	}
	history, err := builds.Generations(ctx, target.ID, "100", "")
	if err != nil {
		t.Fatal(err)
	}
	rollbackID := ""
	for _, g := range history.Items {
		if g.OutputPath == second {
			rollbackID = g.ID
		}
	}
	if rollbackID == "" {
		t.Fatal("previous generation not retained", history)
	}
	staged := submit("cmd_rollback_boot", "deployment.rollback", map[string]any{"generation_id": rollbackID, "mode": "boot", "auto_approve": true}, versions)
	var rollback domain.Deployment
	if json.Unmarshal(staged.Result, &rollback) != nil || rollback.Observation != "staged" || rollback.RunningOutput != candidate.OutputPath || rollback.OutputPath != second {
		t.Fatal(rollback)
	}
	binding, err := hosts.Host(ctx, target.ID)
	if err != nil || *binding.Configuration != *assigned.Configuration {
		t.Fatal("rollback changed desired binding", binding, err)
	}
}

// These target effects are deliberately simulated. The separate VM acceptance
// suite must verify the real NixOS profile, activation unit and reboot behavior.
type fixtureSystem struct {
	mu               sync.Mutex
	running, profile string
	number, starts   int
	history          []domain.Generation
	lostReply        bool
}

func (f *fixtureSystem) set(path string, number int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = path
	f.profile = path
	f.number = number
	f.history = append(f.history, domain.Generation{Number: number, OutputPath: path, Available: true})
}
func (f *fixtureSystem) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.starts }
func (f *fixtureSystem) snapshot() deployment.SystemSnapshot {
	history := append([]domain.Generation{}, f.history...)
	for i := range history {
		history[i].Running = history[i].OutputPath == f.running
	}
	return deployment.SystemSnapshot{Running: f.running, Profile: f.profile, Generation: f.number, ObservedAt: time.Now().UTC(), Generations: history}
}
func (f *fixtureSystem) Inspect(context.Context) (deployment.SystemSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running == "" {
		return deployment.SystemSnapshot{}, domain.Err("OBSERVATION_UNAVAILABLE", "Fixture target not initialized.", 503)
	}
	return f.snapshot(), nil
}
func (f *fixtureSystem) Select(ctx context.Context, output string) (deployment.SystemSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.profile != output {
		f.number++
		f.profile = output
		f.history = append(f.history, domain.Generation{Number: f.number, OutputPath: output, Available: true})
	}
	return f.snapshot(), nil
}
func (f *fixtureSystem) Start(ctx context.Context, task, output, mode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if mode == "switch" {
		f.running = output
	}
	if f.lostReply {
		return domain.Err("OUTCOME_UNKNOWN", "Fixture lost dispatch reply.", 503)
	}
	return nil
}
func (f *fixtureSystem) Activation(context.Context, string) (string, error) { return "succeeded", nil }
func (f *fixtureSystem) Ready(context.Context) bool                         { return true }
