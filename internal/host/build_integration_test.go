package host_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/api"
	"nixmox/internal/client"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/host"
	"nixmox/internal/source"
	"nixmox/internal/task"
	"nixmox/internal/testutil"
	"nixmox/internal/ui/views"
)

func TestBuildMonitorPipeline(t *testing.T) {
	if os.Getenv("NIXMOX_TEST_NIX") != "1" {
		t.Skip("enable real Nix builds with just test-build-monitor")
	}
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	for _, name := range []string{"TASKS", "OUTPUTS", "LOCKS"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: name, Storage: jetstream.MemoryStorage}); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := cluster.OpenStore[domain.Task](ctx, js, "TASKS")
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := cluster.OpenStore[domain.TaskOutput](ctx, js, "OUTPUTS")
	if err != nil {
		t.Fatal(err)
	}
	locks, err := cluster.OpenStore[map[string]string](ctx, js, "LOCKS")
	if err != nil {
		t.Fatal(err)
	}
	logs := cluster.Logs{JS: js}
	if err = logs.Provision(ctx, 1); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "repo")
	repository, err := git.PlainInit(remote, false, git.WithDefaultBranch(plumbing.NewBranchReferenceName("main")))
	if err != nil {
		t.Fatal(err)
	}
	nixpkgs := os.Getenv("NIXMOX_TEST_NIXPKGS")
	if nixpkgs == "" {
		t.Fatal("use just test-build-monitor to supply pinned nixpkgs")
	}
	system := map[string]string{"amd64": "x86_64-linux", "arm64": "aarch64-linux"}[runtime.GOARCH]
	if system == "" {
		t.Skip("unsupported fixture architecture")
	}
	flake := fmt.Sprintf(`{
 inputs.nixpkgs.url = %q;
 outputs = { self, nixpkgs }: let pkgs = import nixpkgs { system = %q; }; in {
  packages.%s.monitor = pkgs.runCommand %q {} "echo monitor-build-line >&2; echo done > $out";
 };
}`, "path:"+nixpkgs, system, system, "nixmox-monitor-"+uuid.NewString())
	if err = os.WriteFile(filepath.Join(remote, "flake.nix"), []byte(flake), 0600); err != nil {
		t.Fatal(err)
	}
	work, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = work.Add("flake.nix"); err != nil {
		t.Fatal(err)
	}
	hash, err := work.Commit("monitor fixture", &git.CommitOptions{Author: &object.Signature{Name: "Example", Email: "example@localhost", When: time.Unix(1, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	repo := source.Repository{ID: "repo", URL: remote, Ref: "main", SHA: hash.String()}
	builder := host.Builder{
		Sources:    source.Manager{Root: filepath.Join(root, "worktrees")},
		Repository: func(context.Context, string) (source.Repository, error) { return repo, nil },
	}
	manager := &task.Manager{HostID: "node", Tasks: tasks, Outputs: outputs, Logs: logs, Resources: task.Resources{Store: locks}, Runners: map[string]task.Runner{"build": builder.Runner}}
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	server := httptest.NewServer((api.Server{
		Health:   func(context.Context) domain.Health { return domain.Health{NodeID: "node", Status: "online"} },
		Services: func() (*api.Services, error) { return &api.Services{Tasks: manager, Logs: logs}, nil },
	}).Handler())
	defer server.Close()
	remoteAPI, err := client.New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer remoteAPI.Close()

	// --- Usage ---
	// -------------
	var submitted domain.Task
	input := host.BuildInput{TargetRepo: source.Pin{ID: repo.ID, Ref: repo.Ref, SHA: repo.SHA}, FlakeDir: ".", Installable: "packages." + system + ".monitor"}
	if err = remoteAPI.Do(ctx, http.MethodPost, "/hosts/node/builds", input, &submitted); err != nil {
		t.Fatal(err)
	}
	var completed domain.Task
	for {
		record, err := tasks.Get(ctx, submitted.ID)
		if err != nil {
			t.Fatal(err)
		}
		completed = record.Value
		if completed.Status.Terminal() {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if completed.Status != domain.Succeeded {
		entries, _ := logs.Read(ctx, "task", completed.ID, 0, 500)
		for _, entry := range entries {
			t.Log(string(entry.Data))
		}
		t.Fatalf("build failed: %+v", completed.Error)
	}
	entries, err := logs.Read(ctx, "task", completed.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("build stderr was not retained")
	}
	model := views.New()
	model.Apply(views.Update{Task: &completed})
	stop := errors.New("replay complete")
	last := fmt.Sprint(entries[len(entries)-1].Sequence)
	err = remoteAPI.Watch(ctx, "/tasks/"+completed.ID+"/logs/stream", "", func(event client.Event) error {
		model.Apply(views.Update{Resource: "tasks/" + completed.ID, EventID: event.ID, Log: event.Data})
		if event.ID == last {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	monitor := model.Task(completed.ID).Build
	if len(monitor.Activities) == 0 || !strings.Contains(strings.Join(monitor.Log.Lines, "\n"), "monitor-build-line") {
		data, _ := json.Marshal(monitor)
		t.Fatalf("real Nix events were not rendered: %s", data)
	}
	if !strings.Contains(monitor.Summary(), "succeeded") {
		t.Fatal("monitor ignored final task state")
	}
}
