package configuration_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"nixmox/internal/cluster"
	"nixmox/internal/config"
	"nixmox/internal/configuration"
	"nixmox/internal/domain"
	"nixmox/internal/gitops"
	"nixmox/internal/gitstore"
	"nixmox/internal/network"
	"nixmox/internal/service"
	"nixmox/internal/state"
	"nixmox/internal/storage"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost")
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
	return string(b)
}
func TestGenerateEditCommitAndReconstructThroughRealGitAndJetStream(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git integration requires dev shell")
	}
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("Nix validation requires dev shell")
	}
	ctx := context.Background()
	remote := t.TempDir()
	git(t, remote, "init", "-b", "main")
	files := map[string]string{
		"flake.nix":                       `{ outputs = { self }: { nixosConfigurations.cedar.config = { fileSystems = {}; }; }; }`,
		".nixmox/templates/server.json":   `{"schema_version":1,"id":"server","kind":"configuration","label":"Server","fields":[{"name":"Name","type":"hostname","required":true},{"name":"Username","type":"text","default":"admin"}],"outputs":[{"path":"hosts/{{ .Name }}.nix","template":"server.gotmpl"}],"configuration":{"name":"{{ .Name }}","flake_directory":".","flake_attribute":"nixosConfigurations.{{ .Name }}"}}`,
		".nixmox/templates/server.gotmpl": `{ ... }: { networking.hostName = {{ nix .Name }}; users.users.{{ nix .Username }}.isNormalUser = true; }`,
	}
	// Missing boot prerequisites are explicit evaluation failures, not builds.
	files["flake.nix"] = `{ outputs = { self }: { nixosConfigurations.cedar.config.system.build.toplevel.drvPath = throw "fileSystems root missing"; }; }`
	for name, content := range files {
		p := filepath.Join(remote, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, remote, "add", ".")
	git(t, remote, "commit", "-m", "remote base")
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
	ops := append(manager.Operations(), sources.Operations()...)
	ops = append(ops, configurations.Operations()...)
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
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
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
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("task timed out", kind)
		return domain.Task{}
	}
	submit("cmd_genesis", "cluster.create", map[string]any{"name": "source-lab"}, nil)
	importPayload := map[string]any{"name": "lab", "remote_url": remote, "upstream_branch": "main"}
	imported := submit("cmd_import", "repository.import", importPayload, nil)
	replay := submit("cmd_import", "repository.import", importPayload, nil)
	if replay.ID != imported.ID {
		t.Fatal("lost retry identity")
	}
	repoID := gitstore.StableID("r_", "lab")
	repo, err := sources.Repository(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	base := *repo.Upstream.Commit
	submit("cmd_generate", "configuration.generate", map[string]any{"repository_id": repoID, "values": map[string]any{"Name": "cedar", "Username": "operator${unsafe}"}}, nil)
	draftID := gitstore.StableID("draft_", "cmd_generate")
	value, err := configurations.Draft(ctx, draftID)
	if err != nil {
		t.Fatal(err)
	}
	draft := value.(configuration.Draft)
	if draft.BaseCommit != base || draft.TemplateID != "server" || draft.Version != 1 {
		t.Fatalf("unpinned draft: %+v", draft)
	}
	submit("cmd_edit", "configuration.draft.edit", map[string]any{"draft_id": draftID, "file": map[string]string{"path": "hosts/cedar.nix", "content": "{ ... }: { networking.hostName = \"cedar\"; }"}}, map[string]int64{"draft:" + draftID: 1})
	submit("cmd_validate", "configuration.validate", map[string]any{"draft_id": draftID}, nil)
	value, err = configurations.Draft(ctx, draftID)
	if err != nil {
		t.Fatal(err)
	}
	draft = value.(configuration.Draft)
	if draft.Validation.State != "incomplete" || draft.Validation.DraftVersion != 2 {
		t.Fatalf("validation: %+v", draft.Validation)
	}
	committed := submit("cmd_commit", "configuration.commit", map[string]any{"draft_id": draftID}, draft.ExpectedVersions)
	if committed.State != "succeeded" {
		t.Fatal(committed)
	}
	repo, err = sources.Repository(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, ok := repo.AcceptedCommands["cmd_commit"]
	if !ok || accepted.ArtifactID == "" || accepted.Commit == base {
		t.Fatal("missing accepted increment", repo)
	}
	art, err := sources.Artifact(ctx, accepted.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if len(art.Prerequisites) == 0 || art.RemoteBaseCommit != base {
		t.Fatal("full base bundle was stored", art)
	}
	receiver, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second := &gitops.Service{State: sources.State, Git: receiver}
	if err = second.Materialize(ctx, repo, accepted.Commit); err != nil {
		t.Fatal(err)
	}
	content, err := receiver.ReadFile(ctx, repo.ID, accepted.Commit, "hosts/cedar.nix")
	if err != nil || string(content) != "{ ... }: { networking.hostName = \"cedar\"; }" {
		t.Fatal("reconstructed source differs", err, string(content))
	}
	// Repeating acceptance after the repository advanced returns the original
	// commit. It must neither create another artifact nor overwrite a ref.
	repeated, err := sources.Accept(ctx, gitops.Change{RepositoryID: repo.ID, RepositoryVersion: 1, CommandID: "cmd_commit"})
	if err != nil || repeated != accepted {
		t.Fatal("acceptance did not reconcile", repeated, err)
	}
	if _, err = sources.Accept(ctx, gitops.Change{RepositoryID: repo.ID, RepositoryVersion: 1, CommandID: "cmd_stale"}); err == nil {
		t.Fatal("stale source mutation accepted")
	}
}
