package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

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

	// Persisted membership cannot silently become a fresh node when recovery
	// material is missing, including before a future configured seed is used.
	node, err := identity.Open(directory, "example")
	if err != nil {
		panic(err)
	}
	node.State.ClusterID = "existing-cluster"
	if err = node.Save(); err != nil {
		panic(err)
	}
	node.Close()
	_, err = daemon.Open(context.Background(), daemon.Config{StateDir: directory})
	fmt.Println("missing cluster state rejected:", err != nil)

	// --- Assertions ---
	// ------------------

	// Output:
	// installer unjoined true
	// installed unjoined true
	// missing cluster state rejected: true
}

func ExampleConfig() {
	// --- Test setup ---
	// ------------------
	settings := viper.New()
	settings.SetConfigType("yaml")
	if err := settings.ReadConfig(strings.NewReader("environment: installed\nnats-port: 4222\n")); err != nil {
		panic(err)
	}
	flags := pflag.NewFlagSet("daemon", pflag.ContinueOnError)
	flags.Int("nats-port", 4222, "NATS client port")
	if err := flags.Parse([]string{"--nats-port=5222"}); err != nil {
		panic(err)
	}
	if err := settings.BindPFlags(flags); err != nil {
		panic(err)
	}

	// --- Usage ---
	// -------------
	var config daemon.Config
	if err := settings.Unmarshal(&config); err != nil {
		panic(err)
	}
	fmt.Println(config.Environment, config.NATSPort)

	// --- Assertions ---
	// ------------------
	// Output:
	// installed 5222
}
