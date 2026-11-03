package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"nixmox/internal/daemon"
	"nixmox/internal/identity"
)

func ExampleOpen() {
	// --- Test setup ---
	// ------------------
	directory, err := os.MkdirTemp("", "nixmox-environment-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(directory)
	var originalID string

	// --- Usage ---
	// -------------
	// The boot environment changes without replacing the persisted node identity.
	for _, environment := range []string{"installer", "installed"} {
		runtime, err := daemon.Open(context.Background(), daemon.Config{
			StateDir: directory, Name: "example", Environment: environment,
		})
		if err != nil {
			panic(err)
		}
		if err = runtime.Close(); err != nil {
			panic(err)
		}
		data, err := os.ReadFile(filepath.Join(directory, "state.json"))
		if err != nil {
			panic(err)
		}
		var state identity.State
		if err = json.Unmarshal(data, &state); err != nil {
			panic(err)
		}
		if originalID == "" {
			originalID = state.NodeID
		}
		fmt.Println(state.Environment, state.Status, state.NodeID == originalID)
	}

	// --- Assertions ---
	// ------------------

	// Output:
	// installer unjoined true
	// installed unjoined true
}
