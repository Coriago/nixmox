package host_test

import (
	"fmt"

	"nixmox/internal/host"
)

func ExampleParseBuildResult() {
	// --- Test setup ---
	// ------------------
	output := []byte(`[{"outputs":{"out":"/nix/store/00000000000000000000000000000000-example"}}]`)

	// --- Usage ---
	// -------------
	result, err := host.ParseBuildResult(output)
	fmt.Println(result.StorePaths, err)
	_, err = host.ParseBuildResult([]byte(`[{"outputs":{"out":"/nix/store/not-a-store-path"}}]`))
	fmt.Println("invalid path rejected:", err != nil)

	// --- Assertions ---
	// ------------------

	// Output:
	// [/nix/store/00000000000000000000000000000000-example] <nil>
	// invalid path rejected: true
}
