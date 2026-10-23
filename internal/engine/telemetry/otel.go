// Package telemetry implements owned OTel pipelines and cached host sampling.
package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/go-logr/logr"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/logging"
)

type OTelConfig struct {
	ServiceName     string
	NodeID          domain.ID
	NodeName        string
	ClusterID       domain.ID
	Endpoint        string // Empty disables export, not local instrumentation.
	MetricsInterval time.Duration
	Logs            io.Writer
	Redact          []string
}

var _ engine.Telemetry = (*OTel)(nil)

type OTel struct {
	Traces      *sdktrace.TracerProvider
	Metrics     *sdkmetric.MeterProvider
	Logs        *sdklog.LoggerProvider
	tracer      trace.Tracer
	propagation propagation.TraceContext
	counters    map[string]metric.Int64Counter
	histograms  map[string]metric.Float64Histogram
	config      OTelConfig
	resources   *resources
}

func NewOTel(ctx context.Context, c OTelConfig) (*OTel, error) {
	if c.ServiceName == "" {
		c.ServiceName = ServiceDaemon
	}
	if (c.ServiceName == ServiceDaemon && c.NodeID == "") || c.MetricsInterval < 250*time.Millisecond {
		return nil, errors.New("telemetry identity and export interval required")
	}
	if c.Logs == nil {
		c.Logs = os.Stdout
	}

	c.Endpoint = strings.TrimRight(c.Endpoint, "/")
	attrs := []attribute.KeyValue{attribute.String("service.name", c.resourceServiceName())}
	if c.ServiceName != ServiceBrowser {
		hostname, err := os.Hostname()
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, attribute.String("host.name", hostname))
	}
	if c.ServiceName == ServiceDaemon {
		attrs = append(attrs, attribute.String("service.instance.id", string(c.NodeID)), attribute.String("nixmox.node.id", string(c.NodeID)))
		if c.NodeName != "" {
			attrs = append(attrs, attribute.String("nixmox.node.name", c.NodeName))
		}
	} else {
		c.ClusterID = ""
	}
	resource := resource.NewWithAttributes("https://opentelemetry.io/schemas/1.37.0", attrs...)
	t := &OTel{resources: &resources{base: resource}, config: c, counters: map[string]metric.Int64Counter{}, histograms: map[string]metric.Float64Histogram{}}
	tp := []sdktrace.TracerProviderOption{sdktrace.WithSampler(sdktrace.ParentBased(rootSampler{}))}
	mp := []sdkmetric.Option{sdkmetric.WithResource(resource), sdkmetric.WithCardinalityLimit(128)}
	var lp []sdklog.LoggerProviderOption
	if c.Endpoint != "" {
		endpoint, err := url.Parse(c.Endpoint)
		if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return nil, errors.New("invalid OTLP endpoint")
		}
		traceExporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(c.Endpoint+"/v1/traces"), otlptracehttp.WithTimeout(domain.ExportTimeout), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
		if err != nil {
			return nil, err
		}
		metricExporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(c.Endpoint+"/v1/metrics"), otlpmetrichttp.WithTimeout(domain.ExportTimeout), otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}))
		if err != nil {
			traceExporter.Shutdown(ctx)
			return nil, err
		}
		logExporter, err := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(c.Endpoint+"/v1/logs"), otlploghttp.WithTimeout(domain.ExportTimeout), otlploghttp.WithRetry(otlploghttp.RetryConfig{Enabled: false}))
		if err != nil {
			traceExporter.Shutdown(ctx)
			metricExporter.Shutdown(ctx)
			return nil, err
		}
		tp = append(tp, sdktrace.WithBatcher(traceExporter, sdktrace.WithMaxQueueSize(1024), sdktrace.WithMaxExportBatchSize(128), sdktrace.WithExportTimeout(domain.ExportTimeout)))
		mp = append(mp, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(resourceMetricExporter{Exporter: metricExporter, resources: t.resources}, sdkmetric.WithInterval(c.MetricsInterval), sdkmetric.WithTimeout(domain.ExportTimeout))))
		lp = append(lp, sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter, sdklog.WithMaxQueueSize(1024), sdklog.WithExportMaxBatchSize(128), sdklog.WithExportTimeout(domain.ExportTimeout))))
	}
	t.resources.traces, t.resources.logs = tp, lp
	t.resources.setCluster(c.ClusterID)
	initial := t.resources.current.Load()
	t.Traces, t.Logs = initial.traces, initial.logs
	t.Metrics = sdkmetric.NewMeterProvider(mp...)
	t.tracer = resourceTracerProvider{TracerProvider: t.Traces, resources: t.resources}.Tracer("nixmox")
	meter := t.Metrics.Meter("nixmox")
	for _, name := range []string{"command.failures", "broker.reconnects", "git.bytes", "artifact.failures", "log.catchup.bytes", "cache.bytes", "cache.failures"} {
		t.counters[name], _ = meter.Int64Counter("nixmox." + name)
	}
	for _, name := range []string{"command.admission.seconds", "task.phase.seconds", "cache.lookup.seconds"} {
		t.histograms[name], _ = meter.Float64Histogram("nixmox." + name)
	}

	return t, nil
}

func (t *OTel) Start(ctx context.Context, name string, origin domain.TraceContext) (context.Context, engine.Span) {
	if len(origin) > 0 {
		parent := t.propagation.Extract(context.Background(), propagation.MapCarrier(origin))
		sc := trace.SpanContextFromContext(parent)
		if sc.IsValid() {
			c, s := t.tracer.Start(ctx, name, trace.WithLinks(trace.Link{SpanContext: sc}))
			return c, otelSpan{s}
		}
	}
	c, s := t.tracer.Start(ctx, name)
	return c, otelSpan{s}
}

// Extract is for synchronous HTTP/NATS request handling; task Start links origins.
func (t *OTel) Extract(ctx context.Context, carrier domain.TraceContext) context.Context {
	return t.propagation.Extract(ctx, propagation.MapCarrier(carrier))
}

func (t *OTel) Inject(ctx context.Context) domain.TraceContext {
	carrier := propagation.MapCarrier{}
	t.propagation.Inject(ctx, carrier)
	return domain.TraceContext(carrier)
}

type otelSpan struct{ trace.Span }

func (s otelSpan) End(err error) { End(s.Span, err) }

func (t *OTel) Logger(ctx context.Context) *slog.Logger {
	return logging.From(ctx)
}

func metricAttributes(labels map[string]string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(labels))
	for _, key := range []string{"operation", "phase", "code", "kind", "status"} {
		if v, ok := labels[key]; ok && len(v) <= 64 {
			attrs = append(attrs, attribute.String(key, v))
		}
	}
	return attrs
}

func (t *OTel) AddCounter(ctx context.Context, name string, n int64, labels map[string]string) {
	if c := t.counters[name]; c != nil {
		c.Add(ctx, n, metric.WithAttributes(metricAttributes(labels)...))
	}
}

func (t *OTel) Observe(ctx context.Context, name string, v float64, labels map[string]string) {
	if h := t.histograms[name]; h != nil {
		h.Record(ctx, v, metric.WithAttributes(metricAttributes(labels)...))
	}
}

func (t *OTel) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, domain.ExportTimeout)
	defer cancel()
	// All signals share one shutdown budget, but a slow exporter cannot consume
	// the entire budget before the other providers even begin to flush.
	results := make(chan error, 3)
	go func() { results <- t.Logs.Shutdown(ctx) }()
	go func() { results <- t.Metrics.Shutdown(ctx) }()
	go func() { results <- t.Traces.Shutdown(ctx) }()
	var err error
	for range 3 {
		select {
		case result := <-results:
			err = errors.Join(err, result)
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
	}
	return err
}

// Install makes this process-owned pipeline available to libraries and callers.
// Call once during application startup, before starting HTTP/NATS/workers.
func (t *OTel) Install() error {
	otel.SetTracerProvider(resourceTracerProvider{TracerProvider: t.Traces, resources: t.resources})
	otel.SetMeterProvider(t.Metrics)
	otel.SetLoggerProvider(resourceLoggerProvider{LoggerProvider: t.Logs, resources: t.resources})
	// Baggage is intentionally excluded: untrusted arbitrary values must not
	// travel through durable commands or become sensitive telemetry.
	otel.SetTextMapPropagator(propagation.TraceContext{})
	local := logging.Configure(t.config.Logs, otelslog.NewHandler("nixmox"), t.config.Redact)
	logger := slog.Default().With("service.name", t.config.resourceServiceName())
	if t.config.ServiceName == ServiceDaemon {
		logger = logger.With("nixmox.node.id", string(t.config.NodeID))
	}
	slog.SetDefault(logger)
	// Includes SDK bounded-queue drop counts, without exporting recursively.
	otel.SetLogger(logr.FromSlogHandler(local.Handler()))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { local.Error("telemetry exporter failed", "error", err) }))
	return otelruntime.Start(otelruntime.WithMeterProvider(t.Metrics))
}
