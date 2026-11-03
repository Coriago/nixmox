package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/api"
	"nixmox/internal/client"
	"nixmox/internal/cluster"
	"nixmox/internal/configuration"
	"nixmox/internal/domain"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
	"nixmox/internal/testutil"
)

func TestRecipeConfigurationAPI(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	versions := &recipe.Service{Repository: recipe.Repository{Records: records}}
	configurations := &configuration.Service{Repository: configuration.Repository{Records: configs}, Recipes: versions.Repository}
	server := httptest.NewServer((api.Server{Health: func(context.Context) domain.Health { return domain.Health{Status: "online"} }, Services: func() (*api.Services, error) {
		return &api.Services{Recipes: versions, Configurations: configurations}, nil
	}}).Handler())
	defer server.Close()
	remote, err := client.New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	definition := recipe.Definition{FlakeDir: ".", FlakeConfiguration: "{{ .Name }}", Fields: map[string]recipe.Field{"Name": {Type: "text"}}}

	// --- Usage ---
	// -------------
	var reusable recipe.Version
	if err = remote.Do(ctx, http.MethodPost, "/recipes", map[string]any{"sourceDefinition": definition}, &reusable); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"recipe":     reusable.Ref(),
		"targetRepo": source.Pin{ID: "repo", Ref: "main", SHA: strings.Repeat("1", 40)},
		"fields":     map[string]any{"Name": "cedar"},
	}
	var first, second, updated, original configuration.Version
	if err = remote.Do(ctx, http.MethodPost, "/configurations", input, &first); err != nil {
		t.Fatal(err)
	}
	input["fields"] = map[string]any{"Name": "birch"}
	if err = remote.Do(ctx, http.MethodPost, "/configurations", input, &second); err != nil {
		t.Fatal(err)
	}
	update := map[string]any{
		"expectedVersion": first.Version,
		"fields":          map[string]any{"Name": "oak"},
	}
	if err = remote.Do(ctx, http.MethodPut, "/configurations/"+first.ID, update, &updated); err != nil {
		t.Fatal(err)
	}
	if err = remote.Do(ctx, http.MethodGet, fmt.Sprintf("/configurations/%s/versions/%d", first.ID, first.Version), nil, &original); err != nil {
		t.Fatal(err)
	}
	var page client.Page[recipe.Version]
	if err = remote.Do(ctx, http.MethodGet, "/recipes?limit=10", nil, &page); err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if first.ID == second.ID {
		t.Fatal("configuration realizations share an identity")
	}
	if original.Collected.Fields["Name"] != "cedar" || second.Collected.Fields["Name"] != "birch" {
		t.Fatal("updating one configuration changed another version")
	}
	if updated.Collected.Fields["Name"] != "oak" || updated.Version != 2 {
		t.Fatal("configuration update did not create the expected version")
	}
	if len(page.Items) != 1 || page.Items[0].Version != 1 || *updated.Recipe != *second.Recipe {
		t.Fatal("configuration edits changed the reusable recipe")
	}
	// Unknown legacy recipe inputs must not be silently discarded.
	if err = remote.Do(ctx, http.MethodPost, "/recipes", input, nil); err == nil {
		t.Fatal("combined legacy recipe payload accepted")
	}
}
