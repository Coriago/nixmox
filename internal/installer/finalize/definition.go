// Package finalize owns post-install preservation and boot finalization.
package finalize

import (
	"context"

	"nixmox/internal/installer/installers"
)

type Definition struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Method string `json:"method"`
}

type Installation struct {
	Target     installers.Target
	Root       string `json:"root"`
	SystemPath string `json:"systemPath"`
	// Preserve writes private identity/network/task state into the installed root.
	// It is required; finalization must never silently discard enrollment.
	Preserve func(context.Context, string) error `json:"-"`
}

type Method interface {
	Definition() Definition
	Check(Installation) installers.Check
	Execute(context.Context, Installation) error
}
