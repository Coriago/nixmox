package comms

import (
	"context"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"nixmox/internal/domain"
	"nixmox/internal/engine/telemetry"
)

func messageSpan(ctx context.Context, destination, operation string, kind trace.SpanKind) (context.Context, trace.Span) {
	return telemetry.Start(ctx, "nats."+destination+"."+operation, trace.WithSpanKind(kind), trace.WithAttributes(
		attribute.String("messaging.system", "nats"), attribute.String("messaging.destination.template", "nx.{cluster}."+destination+".{node}"), attribute.String("messaging.operation.name", operation)))
}

// Query names come from the fixed protocol vocabulary, never arbitrary input.
func querySpanName(action, kind string) string {
	switch kind {
	case "membership.topology", "membership.route", "membership.prepare_leave", "power.authorize", "power.inspect", "command.admit", "task.state", "task.logs", "read":
	default:
		kind = "unknown"
	}
	return "nats." + action + " " + kind
}
func injectHeaders(ctx context.Context, header nats.Header) {
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(header))
}
func headerTrace(header nats.Header) domain.TraceContext {
	ctx := propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(header))
	return telemetry.Inject(ctx)
}
