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
	"nixmox/internal/domain"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
	"nixmox/internal/testutil"
)

func TestRecipeAPI(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "RECIPES"}); err != nil {
		t.Fatal(err)
	}
	records, err := cluster.OpenStore[recipe.Record](ctx, js, "RECIPES")
	if err != nil {
		t.Fatal(err)
	}
	versions := &recipe.Versions{Records: records}
	server := httptest.NewServer((api.Server{Health: func(context.Context) domain.Health { return domain.Health{Status: "online"} }, Services: func() (*api.Services, error) { return &api.Services{Recipes: versions}, nil }}).Handler())
	defer server.Close()
	remote, err := client.New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	input := map[string]any{"targetRepo": source.Pin{ID: "repo", Ref: "main", SHA: strings.Repeat("1", 40)}, "sourceDefinition": recipe.Definition{FlakeDir: ".", FlakeConfiguration: "{{ .Name }}", Fields: map[string]recipe.Field{"Name": {Type: "text"}}}, "fields": map[string]any{"Name": "cedar"}}

	// --- Usage ---
	// -------------
	var first, updated, original recipe.Record
	if err = remote.Do(ctx, http.MethodPost, "/recipes", input, &first); err != nil {
		t.Fatal(err)
	}
	if err = remote.Do(ctx, http.MethodPut, "/recipes/"+first.ID, map[string]any{"expectedVersion": first.Version, "fields": map[string]any{"Name": "birch"}}, &updated); err != nil {
		t.Fatal(err)
	}
	if err = remote.Do(ctx, http.MethodGet, fmt.Sprintf("/recipes/%s/versions/%d", first.ID, first.Version), nil, &original); err != nil {
		t.Fatal(err)
	}
	var page client.Page[recipe.Record]
	if err = remote.Do(ctx, http.MethodGet, "/recipes?limit=10", nil, &page); err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if original.Collected.Fields["Name"] != "cedar" || updated.Collected.Fields["Name"] != "birch" || updated.Version != first.Version+1 {
		t.Fatal("recipe versions changed in place")
	}
	if len(page.Items) != 1 || page.Items[0].Version != updated.Version {
		t.Fatal("list did not project the latest version")
	}
}
