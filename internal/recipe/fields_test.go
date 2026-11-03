package recipe_test

import (
	"context"
	"errors"
	"fmt"

	"nixmox/internal/recipe"
)

func ExampleField_Validate() {
	// --- Test setup ---
	// ------------------
	field := recipe.Field{
		Type:        "multiselect",
		Values:      "{{ .disks }}",
		OptionValue: "{{ .path }}",
	}
	inputs := map[string]any{
		"disks": []any{map[string]any{"path": "/dev/sda"}},
	}

	// --- Usage ---
	// -------------
	ctx := context.Background()
	fmt.Println(field.Validate(ctx, []any{"/dev/sda"}, inputs))
	err := field.Validate(ctx, []any{"/dev/missing"}, inputs)
	fmt.Println("invalid selection:", errors.Is(err, recipe.ErrInvalid))

	// --- Assertions ---
	// ------------------

	// Output:
	// <nil>
	// invalid selection: true
}
