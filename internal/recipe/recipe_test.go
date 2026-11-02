package recipe_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/nats-io/nats.go/jetstream"
	"go.yaml.in/yaml/v3"
	"nixmox/internal/cluster"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
	"nixmox/internal/testutil"
)

func TestRecipeCollectVersionAndRender(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	for _, bucket := range []string{"RECIPES", "REPOS"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, Storage: jetstream.FileStorage}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := cluster.OpenStore[recipe.Record](ctx, js, "RECIPES")
	if err != nil {
		t.Fatal(err)
	}
	repos, err := cluster.OpenStore[source.Repository](ctx, js, "REPOS")
	if err != nil {
		t.Fatal(err)
	}
	versions := recipe.Versions{Records: records}
	root := t.TempDir()
	remote := filepath.Join(root, "remote")
	repository, err := git.PlainInit(remote, false, git.WithDefaultBranch(plumbing.NewBranchReferenceName("main")))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"configuration.gotmpl": "host={{ .Name }} disk={{ .Disk }}\n", "raw.txt": "{{ preserve literal braces }}\n"} {
		if err = os.WriteFile(filepath.Join(remote, path), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err = tree.Add(path); err != nil {
			t.Fatal(err)
		}
	}
	commit, err := tree.Commit("recipe sources", &git.CommitOptions{Author: &object.Signature{Name: "Example", Email: "example@localhost", When: time.Unix(1, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	repo := source.Repository{ID: "repo1", URL: remote, Ref: "main", SHA: commit.String()}
	if _, err = repos.Create(ctx, repo.ID, repo); err != nil {
		t.Fatal(err)
	}
	pin := source.Pin{ID: repo.ID, Ref: repo.Ref, SHA: repo.SHA}
	sources := source.Manager{Root: filepath.Join(root, "worktrees")}
	collector := recipe.Collector{Repositories: repos, Sources: sources, HostDisks: func(context.Context, string) (any, error) {
		return []map[string]string{{"device": "/dev/example"}}, nil
	}}
	templateText, rawText := "{{ .observations.template }}", "{{ .observations.raw }}"
	definition := recipe.Definition{FlakeDir: ".", FlakeConfiguration: "{{ .Name }}", Fields: map[string]recipe.Field{
		"Name": {Type: "text"}, "TargetHost": {Type: "text"}, "Disk": {Type: "select", Values: "{{ .observations.disks }}", OptionValue: "{{ .device }}"},
	}, Observations: map[string]recipe.Observation{
		"disks":    {Type: "hostDisks", Host: "{{ .TargetHost }}"},
		"template": {Type: "repoFile", Repo: "{{ .targetRepo }}", Path: "configuration.gotmpl"},
		"raw":      {Type: "repoFile", Repo: "{{ .targetRepo }}", Path: "raw.txt"},
	}, Outputs: map[string]recipe.Output{"hosts/{{ .Name }}/configuration.nix": {Template: &templateText}, "vendor/raw.txt": {Raw: &rawText}}}

	// --- Usage ---
	// -------------
	first, err := versions.Create(ctx, recipe.Record{TargetRepo: pin, SourceDefinition: definition, Collected: recipe.Collected{Fields: map[string]any{"Name": "cedar", "TargetHost": "host-a"}}})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := versions.Observe(ctx, first.ID, first.Version, "", collector.Collect)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := versions.Update(ctx, first.ID, observed.Version, recipe.Patch{Fields: map[string]any{"Disk": "/dev/example"}})
	if err != nil {
		t.Fatal(err)
	}
	work, err := sources.Checkout(ctx, repo, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	rendered, err := recipe.Render(ctx, ready, work)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := versions.Update(ctx, ready.ID, ready.Version, recipe.Patch{Fields: map[string]any{"TargetHost": "host-b"}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := versions.Get(ctx, ready.ID, ready.Version)
	if err != nil {
		t.Fatal(err)
	}
	config, err := work.ReadFile("hosts/cedar/configuration.nix")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := work.ReadFile("vendor/raw.txt")
	if err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if first.CanRender || observed.CanRender || !ready.CanRender || changed.CanRender || !original.CanRender {
		t.Fatal("partial collection or immutable version readiness is incorrect")
	}
	if string(config) != "host=cedar disk=/dev/example\n" || string(raw) != "{{ preserve literal braces }}\n" || len(rendered.Files) != 2 {
		t.Fatal("template or raw rendering changed content")
	}
	if _, ok := changed.Collected.Observations["disks"]; ok {
		t.Fatal("changing target host retained stale disks")
	}
	if _, ok := changed.Collected.Fields["Disk"]; ok {
		t.Fatal("changing target host retained stale disk selection")
	}
	if ready.Collected.Observations["template"].Repo.SHA != pin.SHA {
		t.Fatal("observation source pin not captured")
	}
	definition.Fields["TargetHost"] = recipe.Field{Type: "select", Values: "{{ .observations.disks }}"}
	if _, err = versions.Create(ctx, recipe.Record{TargetRepo: pin, SourceDefinition: definition}); err == nil {
		t.Fatal("cyclic recipe accepted")
	}
}

func ExampleUpdateSOPS() {
	// --- Test setup ---
	// ------------------
	public := "age1yt3tfqlfrwdwx0z0ynwplcr6qxcxfaqycuprpmy89nr83ltx74tqdpszlw"
	existing := []byte("# preserve existing providers\ncreation_rules:\n  - path_regex: secrets.yaml\n    pgp: existing-fingerprint\n")

	// --- Usage ---
	// -------------
	updated, err := recipe.UpdateSOPS(existing, recipe.SOPSUpdate{Key: public, AnchorValue: "cedar", Rules: []string{"secrets.yaml"}})
	if err != nil {
		panic(err)
	}
	repeated, err := recipe.UpdateSOPS(updated, recipe.SOPSUpdate{Key: public, AnchorValue: "cedar", Rules: []string{"secrets.yaml"}})
	if err != nil {
		panic(err)
	}
	var decoded map[string]any
	if err = yaml.Unmarshal(repeated, &decoded); err != nil {
		panic(err)
	}
	fmt.Println("valid YAML:", len(decoded) > 0)
	fmt.Println("existing provider retained:", strings.Contains(string(repeated), "pgp: existing-fingerprint"))
	fmt.Println("recipient alias present:", strings.Contains(string(repeated), "*cedar"))
	fmt.Println("idempotent:", string(updated) == string(repeated))

	// --- Assertions ---
	// ------------------

	// Output:
	// valid YAML: true
	// existing provider retained: true
	// recipient alias present: true
	// idempotent: true
}
