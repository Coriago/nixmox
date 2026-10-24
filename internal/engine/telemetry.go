package engine

import (
	"context"

	"nixmox/internal/domain"
)

type HostSampler interface {
	Run(context.Context) error
	// One shared measurement cache; slow filesystem work has its own timestamp.
	Snapshot(context.Context) domain.HostTelemetry
	Subscribe(context.Context) (Subscription[domain.HostTelemetry], error)
}
