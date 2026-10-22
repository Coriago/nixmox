package nixstore

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
)

func TestPinnedEvaluationBuildAndVerifiedOutput(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("real Nix boundary requires the dev shell")
	}
	if _, err := os.Stat("/nix/var/nix/daemon-socket/socket"); err != nil {
		t.Skip("real Nix boundary requires a daemon")
	}
	root, err := filepath.Abs("../..")
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
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	packages, err := exec.CommandContext(ctx, "nix", "eval", "--raw", "--impure", "--expr", "(builtins.getFlake "+quote(root)+").inputs.nixpkgs.outPath").Output()
	if err != nil {
		t.Fatal(err)
	}
	flake := `{
 inputs.nixpkgs.url = ` + quote("path:"+strings.TrimSpace(string(packages))) + `;
 outputs = { self, nixpkgs }: let pkgs = import nixpkgs { system = ` + quote(system) + `; }; in { nixosConfigurations.fixture = {
 inherit pkgs;
 config.system.build.toplevel = pkgs.runCommand "nixmox-system-fixture" {} ''
 mkdir -p "$out/bin"; printf '#!${pkgs.runtimeShell}\nexit 0\n' > "$out/bin/switch-to-configuration"; chmod 755 "$out/bin/switch-to-configuration"
 ''; }; };
}`
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(flake), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "nix", "flake", "lock", "path:"+dir)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	n := Nix{Substituters: []string{"https://cache.nixos.org"}}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err, string(b))
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-b", "main")
	git("add", ".")
	git("commit", "-m", "pinned source")
	commit := git("rev-parse", "HEAD")
	evaluation, err := n.EvaluateGit(ctx, dir, ".", commit, "nixosConfigurations.fixture")
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.System != system || evaluation.InputLockDigest == "" {
		t.Fatal(evaluation)
	}
	if err = os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("invalid changed working file"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := n.EvaluateGit(ctx, dir, ".", commit, "nixosConfigurations.fixture")
	if err != nil || again.Derivation != evaluation.Derivation {
		t.Fatal("evaluation followed mutable working files", again, err)
	}
	tracker := NewTracker("task_fixture", "n_fixture")
	var fixtureLog strings.Builder
	output, err := n.Build(ctx, evaluation, filepath.Join(t.TempDir(), "output"), func(line []byte) {
		tracker.Observe(line)
		if fixtureLog.Len() < 64<<10 {
			fixtureLog.Write(line)
			fixtureLog.WriteByte('\n')
		}
	})
	if err != nil {
		t.Fatal(err, fixtureLog.String())
	}
	tracker.Finish(true)
	progress, activities := tracker.Snapshot()
	if !strings.HasPrefix(output.NarHash, "sha256-") || output.Derivation != evaluation.Derivation || !progress.PlanComplete {
		t.Fatal(output, progress, activities)
	}
	verified, err := n.Info(ctx, output.Path)
	if err != nil || verified.NarHash != output.NarHash {
		t.Fatal("output identity differs", verified, err)
	}
	// This is a tiny derivation fixture; VM system builds remain a separate gate.
}
func TestActivityCredentialsAndProtocolFields(t *testing.T) {
	tr := NewTracker("task_one", "n_builder")
	path := "/nix/store/00000000000000000000000000000000-package-1"
	emit := func(v any) { b, _ := json.Marshal(v); tr.Observe(append([]byte("@nix "), b...)) }
	emit(map[string]any{"action": "start", "id": 1, "type": 108, "fields": []any{path, "https://user:secret@cache.example/nar?token=private"}})
	emit(map[string]any{"action": "start", "id": 2, "type": 101, "parent": 1})
	emit(map[string]any{"action": "result", "id": 2, "type": 105, "fields": []any{10, 20, 0, 0}})
	p, rows := tr.Snapshot()
	b, _ := json.Marshal(rows)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "token") || len(rows) != 1 || rows[0].Source.URL != "https://cache.example" || p.Downloads.BytesReceived != 10 || *p.Downloads.BytesTotal != 20 {
		t.Fatal(string(b), p)
	}
	tr.Finish(false)
	_, rows = tr.Snapshot()
	if rows[0].State == "completed" {
		t.Fatal("failed transfer called completed")
	}
}
