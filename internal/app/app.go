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
	// Daemon composition is not implemented; do not create state or open listeners.
	return domain.Unavailable("daemon engines")
}
