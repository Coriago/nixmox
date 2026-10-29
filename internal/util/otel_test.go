package util

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel"
)

func ExampleStart() {
	// --- Test setup ---
	// ------------------
	var output bytes.Buffer
	logger := slog.Default()
	traces, metrics, logs := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetLoggerProvider()
	propagator, errorHandler := otel.GetTextMapPropagator(), otel.GetErrorHandler()
	defer func() {
		otel.SetTracerProvider(traces)
		otel.SetMeterProvider(metrics)
		otel.SetLoggerProvider(logs)
		otel.SetTextMapPropagator(propagator)
		otel.SetErrorHandler(errorHandler)
		Configure(io.Discard, slog.DiscardHandler, nil)
		slog.SetDefault(logger)
	}()

	// --- Usage ---
	// -------------
	// Start once per process. Omit Endpoint for local-only logging.
	ctx := context.Background()
	shutdown, err := Start(ctx, Config{
		Service: "nixmox", InstanceID: "worker-1", Output: &output,
	})
	if err != nil {
		panic(err)
	}

	ctx, span := otel.Tracer("installer").Start(ctx, "install")
	ctx = With(ctx, "node", "worker-1")
	From(ctx).Info("installation started")
	span.End()
	shutdownErr := shutdown(ctx) // Flush pending telemetry before exiting.

	// --- Assertions ---
	// ------------------
	var record map[string]any
	if err := json.NewDecoder(&output).Decode(&record); err != nil {
		panic(err)
	}
	fmt.Println(record["msg"], record["node"])
	fmt.Println("trace correlated:", record["trace_id"] == span.SpanContext().TraceID().String())
	fmt.Println("span correlated:", record["span_id"] == span.SpanContext().SpanID().String())
	fmt.Println("shutdown:", shutdownErr)

	// Output:
	// installation started worker-1
	// trace correlated: true
	// span correlated: true
	// shutdown: <nil>
}
