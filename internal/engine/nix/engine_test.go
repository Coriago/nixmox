package nix

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
	"nixmox/internal/engine/internal/testutil"
)

func TestNixPlansAndMetadata(t *testing.T) {
	n := Engine{}
	source := domain.Source{RepositoryID: "repo", Commit: domain.Commit(strings.Repeat("a", 40)), FlakeDirectory: ".", FlakeAttribute: "nixosConfigurations.host"}
	plan, err := n.BuildPlan(engine.BuildRequest{Source: source, Worktree: "/private/worktree", System: "x86_64-linux"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(plan.Args, " ")
	for _, required := range []string{string(source.Commit), "git+file:", "--no-update-lock-file", "--no-write-lock-file", "--no-link", "internal-json"} {
		if !strings.Contains(argv, required) {
			t.Fatal("missing pinned build policy", required)
		}
	}
	source.Commit = "main"
	if _, err = n.BuildPlan(engine.BuildRequest{Source: source, Worktree: "/private/worktree", System: "x86_64-linux"}); err == nil {
		t.Fatal("mutable build source")
	}
	output := "/nix/store/" + strings.Repeat("0", 32) + "-nixos-system-host"
	activation, err := n.ActivationPlan(output, domain.Switch)
	if err != nil || activation.Program != output+"/bin/switch-to-configuration" {
		t.Fatal(activation, err)
	}
	if _, err = n.ActivationPlan(output, "install"); err == nil {
		t.Fatal("activation reached installer")
	}
	for _, raw := range []string{`[{"path":"` + output + `","narHash":"sha256-fixture","narSize":42}]`, `{"` + output + `":{"narHash":"sha256-fixture","narSize":42}}`} {
		n.Runner = testutil.RunnerFunc(func(_ context.Context, _ engine.CommandLine, out, stderr io.Writer) (engine.ProcessResult, error) {
			_, err := io.WriteString(out, raw)
			return engine.ProcessResult{}, err
		})
		info, err := n.ObserveOutput(context.Background(), output)
		if err != nil || info.StorePath != output || info.NARSize != 42 {
			t.Fatal(info, err)
		}
	}
}

func TestMissingNARSizeRemainsUnknown(t *testing.T) {
	output := "/nix/store/" + strings.Repeat("0", 32) + "-fixture"
	n := Engine{Runner: testutil.RunnerFunc(func(_ context.Context, _ engine.CommandLine, out, stderr io.Writer) (engine.ProcessResult, error) {
		_, err := io.WriteString(out, `[{"path":"`+output+`","narHash":"sha256-fixture"}]`)
		return engine.ProcessResult{}, err
	})}
	if _, err := n.ObserveOutput(context.Background(), output); err == nil {
		t.Fatal("incomplete output metadata accepted")
	}
	closure, err := n.Closure(context.Background(), output)
	if err != nil || len(closure) != 1 || closure[0].NARSize != nil {
		t.Fatal("unknown size was replaced with zero", err)
	}
}

func TestNixAdapterStructuredEvents(t *testing.T) {
	adapter, err := LogAdapter().New(engine.AdapterContext{TaskID: "task", NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	events := []string{
		`{"action":"start","id":1,"type":104,"fields":[]}`,
		`{"action":"result","id":1,"type":105,"fields":[2,8,3,1]}`,
		`{"action":"start","id":2,"type":108,"fields":["/nix/store/path","https://user:password@cache.example/?token=secret"]}`,
		`{"action":"start","id":3,"parent":2,"type":101,"fields":["https://user:password@cache.example/nar/file?token=secret"]}`,
		`{"action":"result","id":3,"type":105,"fields":[10,100,0,0]}`,
		`{"action":"stop","id":3}`,
	}
	var progress *domain.TaskProgress
	for _, event := range events {
		data := []byte("@nix " + event + "\n")
		if _, err = adapter.Consume(engine.LogRecord{Stream: engine.Stderr, Data: data[:4]}); err != nil {
			t.Fatal(err)
		}
		update, err := adapter.Consume(engine.LogRecord{Stream: engine.Stderr, Data: data[4:]})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(update)
		if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "secret") {
			t.Fatal("source credentials leaked")
		}
		if update.Progress != nil {
			progress = update.Progress
		}
		if strings.Contains(event, `"stop"`) && update.Activities[0].State == "completed" {
			t.Fatal("stop was treated as success")
		}
	}
	if progress == nil || progress.Builds.CompletedDerivations == nil || *progress.Builds.CompletedDerivations != 2 || *progress.Downloads.BytesReceived != 10 {
		t.Fatalf("missing normalized counters: %+v", progress)
	}
	update, err := adapter.Consume(engine.LogRecord{Stream: engine.Stderr, Data: []byte("@nix {\"action\":\"start\",\"id\":99,\"type\":9999}\n")})
	if err != nil || update.Progress == nil || len(update.Progress.Unavailable) == 0 {
		t.Fatal("unknown activity was not degraded", err)
	}
}

func TestStructuredClosureDiff(t *testing.T) {
	path := func(name, version, store string) domain.ClosurePath {
		return domain.ClosurePath{Name: shared.Ptr(name), Version: shared.Ptr(version), StorePath: store, NARSize: shared.Ptr(uint64(10))}
	}
	before := []domain.ClosurePath{path("same", "1", "same"), path("upgrade", "1", "old"), path("rebuild", "1", "r1"), path("removed", "1", "gone"), path("ambiguous", "1", "a1"), path("ambiguous", "2", "a2")}
	after := []domain.ClosurePath{path("same", "1", "same"), path("upgrade", "2", "new"), path("rebuild", "1", "r2"), path("added", "1", "added"), path("ambiguous", "3", "a3")}
	diff, err := (ClosureDiffer{}).Compare(context.Background(), before, after)
	if err != nil {
		t.Fatal(err)
	}
	expected := domain.DiffSummary{Added: 1, Removed: 1, VersionChanged: 1, OutputChanged: 1, Ambiguous: 1, Unchanged: 1}
	if diff.Summary != expected {
		t.Fatalf("summary: %+v", diff.Summary)
	}
	again, _ := (ClosureDiffer{}).Compare(context.Background(), before, after)
	if diff.ID != again.ID {
		t.Fatal("unstable comparison identity")
	}
	after[0].NARSize = nil
	unknown, _ := (ClosureDiffer{}).Compare(context.Background(), before, after)
	if unknown.ClosureSize.AfterBytes != nil {
		t.Fatal("missing size fabricated")
	}
}
