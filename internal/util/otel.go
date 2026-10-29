package util

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Config identifies this process and its optional OTLP/HTTP collector.
type Config struct {
	Service    string
	InstanceID string
	Endpoint   string
	Output     io.Writer
}

// Start installs OTel providers and structured logging at process startup.
// An empty Endpoint disables export; a nil Output defaults to stderr.
// Call shutdown with a live context to flush telemetry before exiting.
func Start(ctx context.Context, c Config) (shutdown func(context.Context) error, err error) {
	res := resource.NewSchemaless(attribute.String("service.name", c.Service), attribute.String("service.instance.id", c.InstanceID))
	traceOptions := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	metricOptions := []sdkmetric.Option{sdkmetric.WithResource(res)}
	logOptions := []sdklog.LoggerProviderOption{sdklog.WithResource(res)}
	var stops []func(context.Context) error
	shutdown = func(ctx context.Context) error {
		var result error
		for _, stop := range stops {
			result = errors.Join(result, stop(ctx))
		}
		return result
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, shutdown(context.WithoutCancel(ctx)))
		}
	}()
	if c.Endpoint != "" {
		base := strings.TrimRight(c.Endpoint, "/")
		traces, e := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(base+"/v1/traces"))
		if e != nil {
			return shutdown, e
		}
		stops = append(stops, traces.Shutdown)
		metrics, e := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(base+"/v1/metrics"))
		if e != nil {
			return shutdown, e
		}
		stops = append(stops, metrics.Shutdown)
		logs, e := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(base+"/v1/logs"))
		if e != nil {
			return shutdown, e
		}
		stops = append(stops, logs.Shutdown)
		traceOptions = append(traceOptions, sdktrace.WithBatcher(traces))
		metricOptions = append(metricOptions, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics)))
		logOptions = append(logOptions, sdklog.WithProcessor(sdklog.NewBatchProcessor(logs)))
	}
	traces := sdktrace.NewTracerProvider(traceOptions...)
	metrics := sdkmetric.NewMeterProvider(metricOptions...)
	logs := sdklog.NewLoggerProvider(logOptions...)
	// Providers own exporter shutdown after successful construction.
	stops = []func(context.Context) error{traces.Shutdown, metrics.Shutdown, logs.Shutdown}
	otel.SetTracerProvider(traces)
	otel.SetMeterProvider(metrics)
	otel.SetLoggerProvider(logs)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if c.Output == nil {
		c.Output = os.Stderr
	}
	local := Configure(c.Output, otelslog.NewHandler("nixmox", otelslog.WithLoggerProvider(logs)), nil)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { local.Error("telemetry export failed", "error", err) }))
	return shutdown, nil
}
