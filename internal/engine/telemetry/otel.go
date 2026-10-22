// Package telemetry implements owned OTel pipelines and cached host sampling.
package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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
)

type OTelConfig struct {
	NodeID          domain.ID
	HostName        string
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
	logger      *slog.Logger
	queue       chan logRecord
	cancel      context.CancelFunc
	done        chan struct{}
	Dropped     atomic.Uint64
	secrets     []string
}

type logRecord struct {
	record slog.Record
	span   trace.SpanContext
}

func NewOTel(ctx context.Context, c OTelConfig) (*OTel, error) {
	if c.NodeID == "" || c.MetricsInterval < 250*time.Millisecond {
		return nil, errors.New("telemetry identity and export interval required")
	}
	if c.Logs == nil {
		c.Logs = io.Discard
	}
	resource := resource.NewWithAttributes("https://opentelemetry.io/schemas/1.37.0", attribute.String("service.name", "nixmox"), attribute.String("host.id", string(c.NodeID)), attribute.String("host.name", c.HostName))
	t := &OTel{queue: make(chan logRecord, 1024), done: make(chan struct{}), secrets: append([]string(nil), c.Redact...), counters: map[string]metric.Int64Counter{}, histograms: map[string]metric.Float64Histogram{}}
	tp := []sdktrace.TracerProviderOption{sdktrace.WithResource(resource)}
	mp := []sdkmetric.Option{sdkmetric.WithResource(resource), sdkmetric.WithCardinalityLimit(128)}
	lp := []sdklog.LoggerProviderOption{sdklog.WithResource(resource)}
	if c.Endpoint != "" {
		endpoint, err := url.Parse(c.Endpoint)
		if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
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
		mp = append(mp, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(c.MetricsInterval), sdkmetric.WithTimeout(domain.ExportTimeout))))
		lp = append(lp, sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter, sdklog.WithMaxQueueSize(1024), sdklog.WithExportMaxBatchSize(128), sdklog.WithExportTimeout(domain.ExportTimeout))))
	}
	t.Traces = sdktrace.NewTracerProvider(tp...)
	t.Metrics = sdkmetric.NewMeterProvider(mp...)
	t.Logs = sdklog.NewLoggerProvider(lp...)
	t.tracer = t.Traces.Tracer("nixmox")
	meter := t.Metrics.Meter("nixmox")
	for _, name := range []string{"command.failures", "broker.reconnects", "git.bytes", "artifact.failures", "log.catchup.bytes", "cache.bytes", "cache.failures"} {
		t.counters[name], _ = meter.Int64Counter("nixmox." + name)
	}
	for _, name := range []string{"command.admission.seconds", "task.phase.seconds", "cache.lookup.seconds"} {
		t.histograms[name], _ = meter.Float64Histogram("nixmox." + name)
	}
	dropped, _ := meter.Int64ObservableCounter("nixmox.telemetry.dropped")
	meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(dropped, int64(t.Dropped.Load()))
		return nil
	}, dropped)
	t.logger = slog.New(&boundedLogHandler{owner: t}).With("service.name", "nixmox", "node_id", string(c.NodeID))
	life, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	local := slog.NewJSONHandler(c.Logs, nil)
	export := otelslog.NewHandler("nixmox", otelslog.WithLoggerProvider(t.Logs))
	go func() {
		defer close(t.done)
		for {
			select {
			case <-life.Done():
				return
			case item := <-t.queue:
				ctx := trace.ContextWithSpanContext(context.Background(), item.span)
				_ = local.Handle(ctx, item.record)
				_ = export.Handle(ctx, item.record)
			}
		}
	}()
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

func (s otelSpan) End(err error) {
	if err != nil {
		s.Span.SetStatus(codes.Error, "operation failed")
	}
	s.Span.End()
}

func (t *OTel) Logger(ctx context.Context) *slog.Logger {
	sc := trace.SpanContextFromContext(ctx)
	return t.logger.With("trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
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
	t.cancel()
	select {
	case <-t.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return errors.Join(t.Logs.Shutdown(ctx), t.Metrics.Shutdown(ctx), t.Traces.Shutdown(ctx))
}

func (t *OTel) sanitize(value string) string {
	for _, secret := range t.secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

type boundedLogHandler struct {
	owner  *OTel
	attrs  []slog.Attr
	groups []string
}

func (h *boundedLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *boundedLogHandler) Handle(ctx context.Context, r slog.Record) error {
	copy := slog.NewRecord(r.Time, r.Level, h.owner.sanitize(r.Message), r.PC)
	clean := func(a slog.Attr) slog.Attr {
		key := strings.ToLower(a.Key)
		if strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "token") || strings.Contains(key, "private_key") {
			return slog.String(a.Key, "[redacted]")
		}
		// Arbitrary structs/groups may contain hidden secrets: don't serialize them.
		if a.Value.Kind() == slog.KindAny || a.Value.Kind() == slog.KindGroup {
			return slog.String(a.Key, "[redacted]")
		}
		if a.Value.Kind() == slog.KindString {
			a.Value = slog.StringValue(h.owner.sanitize(a.Value.String()))
		}
		return a
	}
	attrs := append([]slog.Attr(nil), h.attrs...)
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
	for _, a := range attrs {
		a = clean(a)
		if len(h.groups) > 0 {
			a.Key = strings.Join(h.groups, ".") + "." + a.Key
		}
		copy.AddAttrs(a)
	}
	select {
	case h.owner.queue <- logRecord{copy, trace.SpanContextFromContext(ctx)}:
	default:
		h.owner.Dropped.Add(1)
	}
	return nil
}

func (h *boundedLogHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &boundedLogHandler{owner: h.owner, attrs: append(append([]slog.Attr(nil), h.attrs...), a...), groups: append([]string(nil), h.groups...)}
}

func (h *boundedLogHandler) WithGroup(g string) slog.Handler {
	return &boundedLogHandler{owner: h.owner, attrs: append([]slog.Attr(nil), h.attrs...), groups: append(append([]string(nil), h.groups...), g)}
}

// MetricReader serializes sampling callbacks; producers own the underlying cache.
type MetricReader struct{ mu sync.Mutex }
