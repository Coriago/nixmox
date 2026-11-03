package test_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"nixmox/internal/recipe"
	"nixmox/internal/source"
)

func Example_hostRecipe() {
	// --- Test setup ---
	// ------------------
	// Template usage only: sample observations are NOT live VM collection evidence.
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	data, err := os.ReadFile("repo/.nixmox/recipes/host.yaml")
	must(err)
	var definition recipe.Definition
	must(yaml.Unmarshal(data, &definition))
	definition, err = recipe.Canonical(definition)
	must(err)
	_, err = definition.Order()
	must(err)
	work, err := os.MkdirTemp("", "nixmox-host-recipe-")
	must(err)
	defer os.RemoveAll(work)
	must(os.CopyFS(work, os.DirFS("repo")))
	ctx := context.Background()
	// Minimal hardware module for this template example, not a generated VM snapshot.
	hardware := "{ ... }: { nixpkgs.hostPlatform = \"x86_64-linux\"; }\n"

	// --- Usage ---
	// -------------
	// One definition renders two configurations; neither needs a per-host Nix branch.
	for _, name := range []string{"vm2", "vm3"} {
		data, err = os.ReadFile(filepath.Join("configurations", name+".json"))
		must(err)
		var inputs map[string]any
		must(json.Unmarshal(data, &inputs))
		inputs["TargetHost"] = name + "-node-id"
		inputs["Disk"] = "/dev/disk/by-id/" + name
		inputs["AuthorizedKey"] = "ssh-ed25519 example-public-key"
		inputs["observations"] = map[string]any{
			"hosts":    []any{map[string]any{"id": inputs["TargetHost"], "name": name}},
			"modules":  []any{"testpkg"},
			"disks":    []any{map[string]any{"device": inputs["Disk"], "size": "16G"}},
			"hardware": hardware,
		}
		for field, spec := range definition.Fields {
			must(spec.Validate(ctx, inputs[field], inputs))
		}
		rendered, err := recipe.Render(ctx, definition, inputs, &source.Worktree{Path: work})
		must(err)
		var settings map[string]any
		must(json.Unmarshal([]byte(rendered.Contents["hosts/"+name+"/settings.json"]), &settings))
		fmt.Printf("%s: %s, %s, %d files, hardware retained=%t\n",
			rendered.FlakeConfiguration, settings["address"], settings["disk"], len(rendered.Files),
			rendered.Contents["hosts/"+name+"/hardware.nix"] == hardware)
	}
	// Optional Nix module evaluation, still not a boot/install acceptance test.
	if os.Getenv("NIXMOX_TEST_NIX") == "1" {
		expression := `hosts:
		  assert hosts.vm2.config.networking.hostName == "vm2";
		  assert hosts.vm3.config.networking.hostName == "vm3";
		  assert hosts.vm2.config.services.nixmox.config.nats-host == "192.168.77.12";
		  assert hosts.vm3.config.services.nixmox.config.nats-host == "192.168.77.23";
		  assert hosts.vm2.config.disko.devices.disk.system.device == "/dev/disk/by-id/vm2";
		  assert hosts.vm3.config.disko.devices.disk.system.device == "/dev/disk/by-id/vm3";
		  assert builtins.any (p: p.name == "nixmox-e2e-testpkg") hosts.vm2.config.environment.systemPackages;
		  assert !(builtins.any (p: p.name == "nixmox-e2e-testpkg") hosts.vm3.config.environment.systemPackages);
		  [ hosts.vm2.config.system.build.toplevel.drvPath hosts.vm3.config.system.build.toplevel.drvPath ]`
		output, err := exec.Command("nix", "eval", "--json", "path:"+work+"#configurations.nixos", "--apply", expression).CombinedOutput()
		if err != nil {
			panic(fmt.Errorf("evaluate generated host configurations: %w\n%s", err, output))
		}
	}
	fmt.Println("unobserved disk rejected:", definition.Fields["Disk"].Validate(ctx, "/dev/unobserved", map[string]any{
		"observations": map[string]any{"disks": []any{}},
	}) != nil)

	// --- Assertions ---
	// ------------------

	// Output:
	// vm2: 192.168.77.12, /dev/disk/by-id/vm2, 4 files, hardware retained=true
	// vm3: 192.168.77.23, /dev/disk/by-id/vm3, 4 files, hardware retained=true
	// unobserved disk rejected: true
}
