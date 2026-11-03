package configuration_test

import (
	"context"
	"fmt"

	"nixmox/internal/configuration"
	"nixmox/internal/recipe"
)

func ExampleCollector_Collect() {
	// --- Test setup ---
	// ------------------
	collector := configuration.Collector{
		CredentialIDs: func(context.Context) ([]string, error) {
			return []string{"git-access"}, nil
		},
		HostDisks: func(_ context.Context, host string) (any, error) {
			return []string{host + ":/dev/sda"}, nil
		},
		HostHardware: func(_ context.Context, host string, noFileSystems bool) (string, error) {
			return fmt.Sprintf("%s: noFileSystems=%t", host, noFileSystems), nil
		},
		HostAgeKey: func(_ context.Context, host string) (string, error) {
			return host + ":public-key", nil
		},
	}

	// --- Usage ---
	// -------------
	for _, kind := range []recipe.ObservationType{
		recipe.ClusterCreds,
		recipe.HostDisks,
		recipe.HostHardware,
		recipe.HostSSHtoAgeKey,
	} {
		observation := recipe.Observation{
			Type:          kind,
			Host:          "vm2",
			NoFileSystems: true,
		}
		result, err := collector.Collect(context.Background(), observation)
		if err != nil {
			panic(err)
		}
		fmt.Printf("%s: %v\n", kind, result.Output)
	}

	// A missing host callback must not execute on the daemon's host instead.
	_, err := (configuration.Collector{}).Collect(context.Background(), recipe.Observation{
		Type: recipe.HostDisks,
		Host: "remote-host",
	})
	fmt.Println(err)

	// --- Assertions ---
	// ------------------

	// Output:
	// clusterCreds: [git-access]
	// hostDisks: [vm2:/dev/sda]
	// hostHardware: vm2: noFileSystems=true
	// hostSSHtoAgeKey: vm2:public-key
	// host disk service unavailable
}
