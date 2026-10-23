package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"nixmox/internal/domain"
	"nixmox/internal/logging"
)

// Start uses the global no-op provider until application startup installs an SDK.
// Callers retain the standard OTel span API for attributes, events and links.
func Start(ctx context.Context, name string, options ...trace.SpanStartOption) (context.Context, trace.Span) {
	return otel.Tracer("nixmox").Start(ctx, name, options...)
}

// Background starts one finite maintenance operation, never the worker's
// lifetime. Parent-based sampling keeps its HTTP/NATS children together.
func Background(ctx context.Context, name string) (context.Context, trace.Span) {
	return Start(ctx, name, trace.WithNewRoot(), trace.WithAttributes(attribute.Bool("nixmox.background", true)))
}

type rootSampler struct{}

func (rootSampler) Description() string { return "interactive=100%, background=10%" }
func (rootSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	for _, a := range p.Attributes {
		if a.Key == "nixmox.background" && a.Value.AsBool() {
			return sdktrace.TraceIDRatioBased(0.1).ShouldSample(p)
		}
	}
	return sdktrace.AlwaysSample().ShouldSample(p)
}
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(logging.Error(err))
		span.SetStatus(codes.Error, "operation failed")
	}
	span.End()
}

// Explicit W3C propagation also works before initialization. Never propagate
// baggage or use trace context as identity/authorization.
func Inject(ctx context.Context) domain.TraceContext {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return domain.TraceContext(carrier)
}
func Extract(ctx context.Context, carrier domain.TraceContext) context.Context {
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier(carrier))
}

// Origin resumes normal async causality; recovery creates a new trace with a
// link, never a fabricated continuation of an execution interrupted by a crash.
func Origin(ctx context.Context, carrier domain.TraceContext, recovery bool) (context.Context, []trace.SpanStartOption) {
	parent := Extract(ctx, carrier)
	if !recovery {
		return parent, nil
	}
	options := []trace.SpanStartOption{trace.WithNewRoot()}
	if sc := trace.SpanContextFromContext(parent); sc.IsValid() {
		options = append(options, trace.WithLinks(trace.Link{SpanContext: sc}))
	}
	return ctx, options
}
