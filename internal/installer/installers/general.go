package installers

import "nixmox/internal/source"

type General struct{}

func (General) Kind() string { return "general" }

func (General) Definition(system string, pin source.Pin, flakeDir string) (Definition, error) {
	return definition("general", "General Nixmox installer", system, pin, flakeDir, "installer-kexec")
}

func (General) Check(target Target, definition Definition) Check {
	return platform(target, definition)
}
