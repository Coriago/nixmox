package recipe_test

import (
	"context"
	"fmt"
	"strings"

	"nixmox/internal/recipe"
)

func ExampleEvaluate() {
	// --- Test setup ---
	// ------------------
	inputs := map[string]any{"modules": []any{"base", "testpkg"}, "host": "vm2"}

	// --- Usage ---
	// -------------
	modules, err := recipe.Evaluate(context.Background(), "{{ .modules }}", inputs)
	if err != nil {
		panic(err)
	}
	name, err := recipe.Text(context.Background(), "hosts/{{ .host }}/configuration.nix", inputs)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%T: %v\n", modules, modules)
	fmt.Println(name)

	// --- Assertions ---
	// ------------------

	// Output:
	// []interface {}: [base testpkg]
	// hosts/vm2/configuration.nix
}

func ExampleUpdateSOPS() {
	// --- Test setup ---
	// ------------------
	data := []byte("# retain provider\ncreation_rules:\n  - path_regex: secrets.yaml\n    pgp: existing-fingerprint\n")
	change := recipe.SOPSUpdate{Key: "age1yt3tfqlfrwdwx0z0ynwplcr6qxcxfaqycuprpmy89nr83ltx74tqdpszlw", AnchorValue: "vm2", Rules: []string{"secrets.yaml"}}

	// --- Usage ---
	// -------------
	updated, err := recipe.UpdateSOPS(data, change)
	if err != nil {
		panic(err)
	}
	repeated, err := recipe.UpdateSOPS(updated, change)
	if err != nil {
		panic(err)
	}
	fmt.Println("provider retained:", strings.Contains(string(updated), "pgp: existing-fingerprint"))
	fmt.Println("recipient added:", strings.Contains(string(updated), "*vm2"))
	fmt.Println("idempotent:", string(updated) == string(repeated))

	// --- Assertions ---
	// ------------------

	// Output:
	// provider retained: true
	// recipient added: true
	// idempotent: true
}
