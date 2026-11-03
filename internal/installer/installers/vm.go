package installers

import "nixmox/internal/source"

type VM struct{}

func (VM) Kind() string { return "vm" }

func (VM) Definition(system string, pin source.Pin, flakeDir string) (Definition, error) {
	return definition("vm", "Nixmox VM installer", system, pin, flakeDir, "installer-vm-kexec")
}

func (VM) Check(target Target, definition Definition) Check {
	check := platform(target, definition)
	if !check.Compatible {
		return check
	}
	if target.Virtualization != "kvm" && target.Virtualization != "qemu" {
		return Check{Reason: "VM installer requires QEMU/KVM hardware"}
	}
	check.Preference = 100
	return check
}
