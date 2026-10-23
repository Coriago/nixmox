package telemetry

import (
	"context"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"nixmox/internal/domain"
)

const (
	ServiceDaemon  = "nixmox-daemon"
	ServiceCLI     = "nixmox-cli"
	ServiceTUI     = "nixmox-tui"
	ServiceBrowser = "nixmox-browser"
)

func (c OTelConfig) resourceServiceName() string {
	if c.ServiceName != ServiceDaemon {
		return c.ServiceName
	}
	name := c.NodeName
	if name == "" {
		name = string(c.NodeID)
	}
	return ServiceDaemon + "-" + name
}

// Resources are immutable. Each membership generation owns SDK providers with
// its resource, but shares the process-owned batch processors and exporters.
// Existing spans keep their original resource; buffered records are never
// relabeled. Delegation also covers tracers/loggers cached by instrumentation.
// Only OTel.Shutdown shuts down the shared processors, after workers stop.
type resourceGeneration struct {
	resource *resource.Resource
	traces   *sdktrace.TracerProvider
	logs     *sdklog.LoggerProvider
}
type resources struct {
	mu      sync.Mutex
	current atomic.Pointer[resourceGeneration]
	cluster domain.ID
	base    *resource.Resource
	traces  []sdktrace.TracerProviderOption
	logs    []sdklog.LoggerProviderOption
}

func (r *resources) setCluster(cluster domain.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current.Load() != nil && r.cluster == cluster {
		return
	}
	attrs := r.base.Attributes()
	if cluster != "" {
		attrs = append(attrs, attribute.String("nixmox.cluster.id", string(cluster)))
	}
	resource := resource.NewWithAttributes(r.base.SchemaURL(), attrs...)
	tp := append([]sdktrace.TracerProviderOption{sdktrace.WithResource(resource)}, r.traces...)
	lp := append([]sdklog.LoggerProviderOption{sdklog.WithResource(resource)}, r.logs...)
	r.current.Store(&resourceGeneration{resource: resource, traces: sdktrace.NewTracerProvider(tp...), logs: sdklog.NewLoggerProvider(lp...)})
	r.cluster = cluster
}

// SetCluster is called after local membership has been durably installed or
// retired. Empty means unjoined; this is not the broker's standalone pseudo-ID.
func (t *OTel) SetCluster(cluster domain.ID) {
	if t.config.ServiceName == ServiceDaemon {
		t.resources.setCluster(cluster)
	}
}

type resourceTracerProvider struct {
	trace.TracerProvider
	resources *resources
}

func (p resourceTracerProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return resourceTracer{Tracer: p.TracerProvider.Tracer(name, opts...), resources: p.resources, name: name, options: opts}
}

type resourceTracer struct {
	trace.Tracer
	resources *resources
	name      string
	options   []trace.TracerOption
}

func (t resourceTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return t.resources.current.Load().traces.Tracer(t.name, t.options...).Start(ctx, name, opts...)
}

type resourceLoggerProvider struct {
	log.LoggerProvider
	resources *resources
}

func (p resourceLoggerProvider) Logger(name string, opts ...log.LoggerOption) log.Logger {
	return resourceLogger{Logger: p.LoggerProvider.Logger(name, opts...), resources: p.resources, name: name, options: opts}
}

type resourceLogger struct {
	log.Logger
	resources *resources
	name      string
	options   []log.LoggerOption
}

func (l resourceLogger) Emit(ctx context.Context, record log.Record) {
	l.resources.current.Load().logs.Logger(l.name, l.options...).Emit(ctx, record)
}
func (l resourceLogger) Enabled(ctx context.Context, params log.EnabledParameters) bool {
	return l.resources.current.Load().logs.Logger(l.name, l.options...).Enabled(ctx, params)
}

// Metrics describe the process at collection/export time. Keep the SDK meter
// and its instruments/callbacks alive across membership changes. Cumulative
// counters remain process-lifetime counters; already exported points stay put.
type resourceMetricExporter struct {
	sdkmetric.Exporter
	resources *resources
}

func (e resourceMetricExporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	snapshot := *data
	snapshot.Resource = e.resources.current.Load().resource
	return e.Exporter.Export(ctx, &snapshot)
}
