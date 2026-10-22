// Package app is the composition root; engines never depend on it.
package app

import (
	"context"
	"nixmox/internal/domain"
	"nixmox/internal/runtime"
)

func Serve(ctx context.Context, config runtime.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := config.Validate(); err != nil {
		return err
	}
	// Compose identity/local store -> TLS broker -> telemetry/tasks -> services ->
	// gateway. Bind every required listener before readiness; shut down in reverse.
	// Until those boundaries are implemented, do not create state or open listeners.
	return domain.Unavailable("daemon engines")
}
