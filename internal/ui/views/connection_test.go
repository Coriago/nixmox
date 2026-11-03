package views

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/api"
	"nixmox/internal/client"
	"nixmox/internal/cluster"
	"nixmox/internal/configuration"
	"nixmox/internal/domain"
	"nixmox/internal/recipe"
	"nixmox/internal/testutil"
)

func TestVersionedConsoleWorkflow(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	for _, bucket := range []string{"RECIPES", "CONFIGURATIONS"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := cluster.OpenStore[recipe.Version](ctx, js, "RECIPES")
	if err != nil {
		t.Fatal(err)
	}
	configs, err := cluster.OpenStore[configuration.Version](ctx, js, "CONFIGURATIONS")
	if err != nil {
		t.Fatal(err)
	}
	recipes := &recipe.Service{Repository: recipe.Repository{Records: records}}
	configurations := &configuration.Service{Repository: configuration.Repository{Records: configs}, Recipes: recipes.Repository}
	server := httptest.NewServer((api.Server{
		Services: func() (*api.Services, error) {
			return &api.Services{Recipes: recipes, Configurations: configurations}, nil
		},
	}).Handler())
	defer server.Close()
	remote, err := client.New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	model := New()
	model.Apply(Update{Snapshot: &Snapshot{Health: domain.Health{NodeID: "node", Status: "online"}}})
	submit := func() {
		t.Helper()
		model.Dispatch(Action{Kind: "source-form-save"})
		request := model.TakeCommand()
		if request == nil {
			t.Fatalf("form did not produce a request: %s", model.Notice)
		}
		var response json.RawMessage
		if err := remote.Do(ctx, request.Method, request.Path, request.Body, &response); err != nil {
			t.Fatal(err)
		}
		model.Apply(Update{Submitted: true, Target: request.Target, Response: response})
	}

	// --- Usage ---
	// -------------
	model.Dispatch(Action{Kind: "recipe-create"})
	model.SetFormValue(model.Form.ID, "json", `{"name":"Reusable","sourceDefinition":{"flakeDir":".","flakeConfiguration":"{{ .Name }}","fields":{"Name":{"type":"text"}}}}`)
	submit()
	reference := recipe.Reference{ID: model.Document.ID, Version: model.Document.Version}
	model.Dispatch(Action{Kind: "page", ID: "Configurations"})
	model.Dispatch(Action{Kind: "configuration-create"})
	payload, _ := json.Marshal(map[string]any{"name": "vm2", "recipe": reference, "targetRepo": map[string]string{"id": "repo", "ref": "main", "sha": "1111111111111111111111111111111111111111"}, "fields": map[string]any{"Name": "vm2"}})
	model.SetFormValue(model.Form.ID, "json", string(payload))
	submit()
	id := model.Document.ID
	model.Dispatch(Action{Kind: "configuration-inputs"})
	model.SetFormValue(model.Form.ID, "json", `{"expectedVersion":1,"fields":{"Name":"vm3"}}`)
	submit()
	model.Dispatch(Action{Kind: "show-version"})
	model.SetFormValue(model.Form.ID, "version", "1")
	submit()

	// --- Assertions ---
	// ------------------
	if model.Document.ID != id || model.Document.Version != 1 {
		t.Fatal("console did not load the selected immutable version")
	}
	latest, err := configurations.Latest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != 2 || latest.Collected.Fields["Name"] != "vm3" {
		t.Fatal("input form did not persist a new version")
	}
	unchanged, err := recipes.Latest(ctx, reference.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != 1 {
		t.Fatal("configuration form modified its recipe")
	}
}
