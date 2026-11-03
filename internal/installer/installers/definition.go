// Package installers defines built-in installer profiles and their compatibility checks.
package installers

import (
	"errors"
	"fmt"
	"path/filepath"

	"nixmox/internal/host"
	"nixmox/internal/source"
)

type Target struct {
	NodeID         string `json:"nodeId"`
	System         string `json:"system"`
	Environment    string `json:"environment"`
	BootID         string `json:"bootId"`
	SystemPath     string `json:"systemPath"`
	Firmware       string `json:"firmware"`
	Virtualization string `json:"virtualization"`
	Container      bool   `json:"container"`
	KexecAvailable bool   `json:"kexecAvailable"`
}

type Check struct {
	Compatible bool   `json:"compatible"`
	Preference int    `json:"preference"`
	Reason     string `json:"reason,omitempty"`
}

type Definition struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	System string          `json:"system"`
	Build  host.BuildInput `json:"build"`
}

type Installer interface {
	Kind() string
	Definition(string, source.Pin, string) (Definition, error)
	Check(Target, Definition) Check
}

func Builtins() []Installer { return []Installer{General{}, VM{}} }

func Lookup(kind string) (Installer, error) {
	for _, profile := range Builtins() {
		if profile.Kind() == kind {
			return profile, nil
		}
	}
	return nil, fmt.Errorf("unknown installer kind %q", kind)
}

func definition(kind, name, system string, pin source.Pin, flakeDir, attribute string) (Definition, error) {
	if system != "x86_64-linux" && system != "aarch64-linux" {
		return Definition{}, errors.New("unsupported installer system")
	}
	if err := pin.Validate(); err != nil {
		return Definition{}, err
	}
	if flakeDir == "" {
		flakeDir = "."
	}
	if !filepath.IsLocal(flakeDir) {
		return Definition{}, errors.New("flake directory must be relative")
	}
	return Definition{Name: name, Kind: kind, System: system, Build: host.BuildInput{
		TargetRepo: pin, FlakeDir: flakeDir, Installable: "packages." + system + "." + attribute,
	}}, nil
}

func platform(target Target, definition Definition) Check {
	switch {
	case target.Container:
		return Check{Reason: "installer transitions cannot run in a container"}
	case target.System != definition.System:
		return Check{Reason: "installer architecture differs from host"}
	case target.System != "x86_64-linux" && target.System != "aarch64-linux":
		return Check{Reason: "unsupported host system"}
	case target.System == "aarch64-linux" && target.Firmware != "uefi":
		return Check{Reason: "generic ARM64 installer requires a UEFI platform; board-specific installer needed"}
	default:
		return Check{Compatible: true, Preference: 10}
	}
}
