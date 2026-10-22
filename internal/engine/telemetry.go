package engine

import (
	"context"
	"io"
	"log/slog"

	"nixmox/internal/domain"
)

type Span interface {
	End(error)
}
type Telemetry interface {
	Start(context.Context, string, domain.TraceContext) (context.Context, Span)
	Inject(context.Context) domain.TraceContext
	Logger(context.Context) *slog.Logger
	AddCounter(context.Context, string, int64, map[string]string)
	Observe(context.Context, string, float64, map[string]string)
	Shutdown(context.Context) error
}
type HostSampler interface {
	Run(context.Context) error
	// One shared measurement cache; slow filesystem work has its own timestamp.
	Snapshot(context.Context) domain.HostTelemetry
	Subscribe(context.Context) (Subscription[domain.HostTelemetry], error)
}
type Diagnostics interface {
	Collect(context.Context, domain.HostDiagnostics) (domain.Diagnostic, error)
	Download(context.Context, domain.ID) (domain.Download, io.ReadCloser, error)
}
