package configuration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/nats-io/nats.go/jetstream"
	"go.yaml.in/yaml/v3"
	"nixmox/internal/cluster"
	"nixmox/internal/configuration"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
	"nixmox/internal/testutil"
)

func TestRecipeConfigurationLifecycle(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	for _, bucket := range []string{"RECIPES", "CONFIGURATIONS", "REPOS"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatal(err)
		}
	}
	recipeRecords, err := cluster.OpenStore[recipe.Version](ctx, js, "RECIPES")
	if err != nil {
		t.Fatal(err)
	}
	configRecords, err := cluster.OpenStore[configuration.Version](ctx, js, "CONFIGURATIONS")
	if err != nil {
		t.Fatal(err)
	}
	repos, err := cluster.OpenStore[source.Repository](ctx, js, "REPOS")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote")
	gitRepo, err := git.PlainInit(remote, false, git.WithDefaultBranch(plumbing.NewBranchReferenceName("main")))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := gitRepo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	template := "{{ .observations.template }}"
	definition := recipe.Definition{
		FlakeDir:           ".",
		FlakeConfiguration: "{{ .Name }}",
		Fields: map[string]recipe.Field{
			"Name": {Type: "text"},
			"Host": {Type: "text"},
			"Disk": {
				Type:        "select",
				Values:      "{{ .observations.disks }}",
				OptionValue: "{{ .device }}",
			},
			"Mode": {Type: "select", Values: []any{1, 2}},
		},
		Observations: map[string]recipe.Observation{
			"disks":    {Type: "hostDisks", Host: "{{ .Host }}"},
			"template": {Type: "repoFile", Repo: "{{ .recipeSource }}", Path: "host.gotmpl"},
		},
		Outputs: map[string]recipe.Output{
			"hosts/{{ .Name }}/configuration.nix": {Template: &template},
		},
	}
	commit := func() string {
		t.Helper()
		data, err := yaml.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string][]byte{"recipe.yaml": data, "host.gotmpl": []byte("host={{ .Name }} disk={{ .Disk }}\n")} {
			if err = os.WriteFile(filepath.Join(remote, name), content, 0644); err != nil {
				t.Fatal(err)
			}
			if _, err = tree.Add(name); err != nil {
				t.Fatal(err)
			}
		}
		hash, err := tree.Commit("recipe definition", &git.CommitOptions{Author: &object.Signature{Name: "Example", Email: "example@localhost", When: time.Unix(1, 0)}})
		if err != nil {
			t.Fatal(err)
		}
		return hash.String()
	}
	pin := source.Pin{ID: "repo", Ref: "main", SHA: commit()}
	repo := source.Repository{ID: pin.ID, Ref: pin.Ref, SHA: pin.SHA, URL: remote}
	if _, err = repos.Create(ctx, repo.ID, repo); err != nil {
		t.Fatal(err)
	}
	sources := source.Manager{Root: filepath.Join(root, "worktrees")}
	recipes := recipe.Service{Repository: recipe.Repository{Records: recipeRecords}, Sources: sources, Repositories: repos}
	calls := 0
	collector := configuration.Collector{Sources: sources, Repositories: repos, HostDisks: func(_ context.Context, host string) (any, error) {
		calls++
		return []map[string]string{{"device": "/dev/" + host}}, nil
	}}
	configs := configuration.Service{
		Repository:   configuration.Repository{Records: configRecords},
		Recipes:      recipes.Repository,
		Observer:     collector,
		Sources:      sources,
		Repositories: repos,
	}

	// --- Usage ---
	// -------------
	imported, err := recipes.Import(ctx, "VM", recipe.Source{Pin: pin, Path: "recipe.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	// Repository observations use the same pinned source without changing it.
	commandResult, err := collector.Collect(ctx, recipe.Observation{
		Type: recipe.RepoCommand,
		Repo: pin,
		Cmd:  `test -f recipe.yaml && printf '["base","testpkg"]'`,
	})
	if err != nil {
		t.Fatal(err)
	}
	repositoryResult, err := collector.Collect(ctx, recipe.Observation{Type: recipe.ClusterRepos})
	if err != nil {
		t.Fatal(err)
	}

	unchanged, err := recipes.Sync(ctx, imported.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref := imported.Ref()
	first, err := configs.Create(ctx, configuration.Create{Name: "vm2", Recipe: &ref, TargetRepo: pin, Fields: map[string]any{"Name": "vm2", "Host": "vm2", "Mode": 1}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := configs.Create(ctx, configuration.Create{Name: "vm3", Recipe: &ref, TargetRepo: pin, Fields: map[string]any{"Name": "vm3", "Host": "vm3", "Mode": 2}})
	if err != nil {
		t.Fatal(err)
	}
	first, err = configs.Observe(ctx, first.ID, first.Version, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err = configs.Observe(ctx, second.ID, second.Version, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err = configs.SetInputs(ctx, first.ID, first.Version, map[string]any{"Disk": "/dev/vm2"})
	if err != nil {
		t.Fatal(err)
	}
	second, err = configs.SetInputs(ctx, second.ID, second.Version, map[string]any{"Disk": "/dev/vm3"})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := configs.Render(ctx, first.ID, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	// Deployment consumes exactly the preview in a separately owned worktree.
	work, directory, configurationName, err := configs.Materialize(ctx, first.ID, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	materialized, err := os.ReadFile(filepath.Join(directory, "hosts/vm2/configuration.nix"))
	if err != nil {
		t.Fatal(err)
	}
	rename := "Custom VM"
	fork, err := recipes.Edit(ctx, imported.ID, imported.Version, recipe.Edit{Name: &rename})
	if err != nil {
		t.Fatal(err)
	}
	rename = "Custom VM edited"
	edited, err := recipes.Edit(ctx, fork.ID, fork.Version, recipe.Edit{Name: &rename})
	if err != nil {
		t.Fatal(err)
	}
	forkRef := edited.Ref()
	detached, err := configs.UpgradeRecipe(ctx, second.ID, second.Version, forkRef)
	if err != nil {
		t.Fatal(err)
	}
	detachedRender, err := configs.Render(ctx, detached.ID, detached.Version)
	if err != nil {
		t.Fatal(err)
	}
	definition.Fields["Enabled"] = recipe.Field{Type: "boolean"}
	definition.Observations["disks"] = recipe.Observation{Type: "hostDisks", Host: `{{ printf "%s" .Host }}`}
	commit()
	synced, err := recipes.Sync(ctx, imported.ID, imported.Version)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := configs.UpgradeRecipe(ctx, first.ID, first.Version, synced.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.CanRender || len(upgraded.Collected.Observations) != 0 {
		t.Fatal("recipe upgrade retained changed observation evidence")
	}
	if _, exists := upgraded.Collected.Fields["Disk"]; exists {
		t.Fatal("recipe upgrade retained dependent disk selection")
	}
	original, err := configs.Get(ctx, first.ID, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	latestSecond, err := configs.Latest(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = configs.SetInputs(ctx, upgraded.ID, upgraded.Version, map[string]any{"Enabled": "wrong type"}); err == nil {
		t.Fatal("invalid submission accepted")
	}
	upgraded, err = configs.SetInputs(ctx, upgraded.ID, upgraded.Version, map[string]any{"Enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err = configs.Observe(ctx, upgraded.ID, upgraded.Version, "")
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err = configs.SetInputs(ctx, upgraded.ID, upgraded.Version, map[string]any{"Disk": "/dev/vm2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = configs.SetInputs(ctx, upgraded.ID, first.Version, map[string]any{"Name": "stale"}); err == nil {
		t.Fatal("stale configuration edit accepted")
	}

	// --- Assertions ---
	// ------------------
	modules, ok := commandResult.Output.([]any)
	if !ok || len(modules) != 2 || modules[0] != "base" || modules[1] != "testpkg" || commandResult.Repo == nil || *commandResult.Repo != pin {
		t.Fatal("repository command lost its JSON output or immutable source pin")
	}
	repositories, ok := repositoryResult.Output.([]any)
	if !ok || len(repositories) != 1 {
		t.Fatal("repository observation did not list the registered repository")
	}
	if unchanged.Version != 1 || synced.Version != 2 {
		t.Fatal("sync changed an unchanged definition or failed to version a changed one")
	}
	if fork.ID == imported.ID || fork.RecipeSource != nil || fork.Version != 1 || edited.ID != fork.ID || edited.Version != 2 {
		t.Fatal("tracked fork/detached version semantics failed")
	}
	if fork.Dependencies["recipeSource"] != pin || fork.DerivedFrom == nil || *fork.DerivedFrom != ref {
		t.Fatal("fork lost pinned dependencies or provenance")
	}
	if !original.CanRender || !upgraded.CanRender || original.Recipe.Version != 1 || latestSecond.Recipe.ID != fork.ID {
		t.Fatal("upgrade changed another immutable configuration")
	}
	if rendered.Contents["hosts/vm2/configuration.nix"] != "host=vm2 disk=/dev/vm2\n" || detachedRender.Contents["hosts/vm3/configuration.nix"] != "host=vm3 disk=/dev/vm3\n" {
		t.Fatal("rendered content or isolated host inputs changed")
	}
	if configurationName != "vm2" || string(materialized) != rendered.Contents["hosts/vm2/configuration.nix"] {
		t.Fatal("deployment worktree differs from its pinned preview")
	}
	if calls != 3 {
		t.Fatalf("rendering/forking unexpectedly recollected observations: %d", calls)
	}
	if _, err = recipes.Sync(ctx, fork.ID, edited.Version); err == nil {
		t.Fatal("detached recipe still tracks repository")
	}
}
