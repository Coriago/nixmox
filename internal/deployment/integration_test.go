package deployment_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	hosts := &host.Catalog{State: sources.State, Identity: manager.Identity, Members: manager.Members, Configurations: configurations, Sample: func() domain.HostSample { return sample }}
	builds := &deployment.Service{Sources: sources, Configurations: configurations, Hosts: hosts, Identity: manager.Identity, Root: local.Root + "/builds", Nix: nixstore.Nix{Substituters: []string{"https://cache.nixos.org"}}}
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
}
