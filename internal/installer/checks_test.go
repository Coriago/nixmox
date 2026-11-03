package installer_test

import (
	"fmt"
	"strings"

	"nixmox/internal/installer"
	"nixmox/internal/installer/finalize"
	"nixmox/internal/installer/installers"
	"nixmox/internal/source"
)

func ExampleSelect() {
	// --- Test setup ---
	// ------------------
	pin := source.Pin{ID: "nixmox", Ref: "main", SHA: strings.Repeat("1", 40)}
	general, err := (installers.General{}).Definition("x86_64-linux", pin, ".")
	if err != nil {
		panic(err)
	}
	vm, err := (installers.VM{}).Definition("x86_64-linux", pin, ".")
	if err != nil {
		panic(err)
	}
	general.ID = "general"
	vm.ID = "vm"
	target := installers.Target{System: "x86_64-linux", Environment: "installed", Virtualization: "kvm", KexecAvailable: true}
	definitions := []installers.Definition{general, vm}

	// --- Usage ---
	// -------------
	_, selected := installer.Select(target, definitions, "", "")
	fmt.Println("automatic:", selected.Installer.ID, selected.Prepare.ID)
	fmt.Println("build:", selected.Installer.Build.Installable)
	_, selected = installer.Select(target, definitions, "general", "prepare-kexec")
	fmt.Println("explicit:", selected.Installer.ID)
	target.System = "aarch64-linux"
	_, selected = installer.Select(target, definitions, "vm", "prepare-kexec")
	fmt.Println("architecture mismatch:", selected == nil)

	// --- Assertions ---
	// ------------------

	// Output:
	// automatic: vm prepare-kexec
	// build: packages.x86_64-linux.installer-vm-kexec
	// explicit: general
	// architecture mismatch: true
}

func Example_finalizerChecks() {
	// --- Test setup ---
	// ------------------
	method := finalize.General{}
	installation := finalize.Installation{
		Target: installers.Target{System: "x86_64-linux", Environment: "installer", Firmware: "uefi"},
		Root:   "/mnt", SystemPath: "/nix/store/" + strings.Repeat("a", 32) + "-nixos-system-test",
	}

	// --- Usage ---
	// -------------
	fmt.Println("mounted destination candidate:", method.Check(installation).Compatible)
	installation.Root = "/"
	fmt.Println("live root:", method.Check(installation).Compatible)
	installation.Root = "/mnt"
	installation.Target.Environment = "installed"
	fmt.Println("installed environment:", method.Check(installation).Compatible)

	// --- Assertions ---
	// ------------------

	// Output:
	// mounted destination candidate: true
	// live root: false
	// installed environment: false
}
