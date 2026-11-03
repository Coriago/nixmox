package recipe_test

import (
	"context"
	"fmt"
	"os"
	"strings"

	"nixmox/internal/recipe"
	"nixmox/internal/source"
)

func ExampleRender() {
	// --- Test setup ---
	// ------------------
	directory, err := os.MkdirTemp("", "recipe-render-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(directory)

	template := "{{ .template }}"
	raw := "{{ .raw }}"
	definition := recipe.Definition{
		FlakeDir:           ".",
		FlakeConfiguration: "{{ .host }}",
		Outputs: map[string]recipe.Output{
			"host.nix": {Template: &template},
			"raw.txt":  {Raw: &raw},
			".sops.yaml": {UpdateSOPS: &recipe.SOPSUpdate{
				Key:         "age1yt3tfqlfrwdwx0z0ynwplcr6qxcxfaqycuprpmy89nr83ltx74tqdpszlw",
				AnchorValue: "{{ .host }}",
				Rules:       []string{"secrets.yaml"},
			}},
		},
	}
	inputs := map[string]any{
		"host":     "vm2",
		"template": "host={{ .host }}",
		"raw":      "literal {{ .host }}",
	}

	// --- Usage ---
	// -------------
	preview, err := recipe.Render(context.Background(), definition, inputs, &source.Worktree{Path: directory})
	if err != nil {
		panic(err)
	}
	fmt.Println(preview.FlakeConfiguration)
	fmt.Println(preview.Files)
	fmt.Println(preview.Contents["host.nix"])
	fmt.Println(preview.Contents["raw.txt"])
	fmt.Println("SOPS recipient:", strings.Contains(preview.Contents[".sops.yaml"], "*vm2"))

	// --- Assertions ---
	// ------------------

	// Output:
	// vm2
	// [.sops.yaml host.nix raw.txt]
	// host=vm2
	// literal {{ .host }}
	// SOPS recipient: true
}
