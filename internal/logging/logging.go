// Package logging provides context-correlated slog without requiring telemetry
// initialization at call sites. Use From(ctx) again when entering a child span.
package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/otel/trace"
)

type loggerKey struct{}

var secrets atomic.Pointer[[]string]

func base(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return logger
	}
	return slog.Default()
}
func From(ctx context.Context) *slog.Logger {
	// Only retain correlation, not request cancellation, bodies or credentials.
	correlation := trace.ContextWithSpanContext(context.Background(), trace.SpanContextFromContext(ctx))
	return slog.New(contextHandler{Handler: base(ctx).Handler(), ctx: correlation})
}

// With attaches structured operation identity, inherited by nested spans/loggers.
func With(ctx context.Context, attrs ...any) context.Context {
	return context.WithValue(ctx, loggerKey{}, base(ctx).With(attrs...))
}

// Configure fans out synchronously to local JSON and to the SDK's bounded batch
// exporter. No second queue or logging worker can lose records during shutdown.
// The returned local-only logger is safe for exporter failures (no recursion).
func Configure(output io.Writer, export slog.Handler, redact []string) *slog.Logger {
	rules := append([]string(nil), redact...)
	secrets.Store(&rules)
	local := &safeHandler{Handler: slog.NewJSONHandler(output, nil)}
	slog.SetDefault(slog.New(&safeHandler{Handler: slog.NewMultiHandler(slog.NewJSONHandler(output, nil), export)}))
	return slog.New(local)
}
func clean(value string) string {
	if rules := secrets.Load(); rules != nil {
		for _, secret := range *rules {
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "[redacted]")
			}
		}
	}
	if len(value) > 8192 {
		value = value[:8192] + " [truncated]"
	}
	return value
}

// Error sanitizes errors before they become exception events as well as logs.
func Error(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(clean(err.Error()))
}
func cleanAttr(a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	for _, sensitive := range []string{"password", "secret", "token", "private_key", "authorization", "cookie", "payload"} {
		if strings.Contains(key, sensitive) {
			return slog.String(a.Key, "[redacted]")
		}
	}
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(clean(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			a.Value = slog.StringValue(clean(err.Error()))
		} else {
			a.Value = slog.StringValue("[redacted]")
		}
	case slog.KindGroup:
		group := a.Value.Group()
		attrs := make([]slog.Attr, 0, min(len(group), 64))
		for _, attr := range group[:min(len(group), 64)] {
			attrs = append(attrs, cleanAttr(attr))
		}
		a.Value = slog.GroupValue(attrs...)
	}
	return a
}

type safeHandler struct{ slog.Handler }

func (h *safeHandler) Handle(ctx context.Context, r slog.Record) error {
	record := slog.NewRecord(r.Time, r.Level, clean(r.Message), r.PC)
	count := 0
	r.Attrs(func(a slog.Attr) bool { record.AddAttrs(cleanAttr(a)); count++; return count < 64 })
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, record)
}
func (h *safeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, 0, min(len(attrs), 64))
	for _, a := range attrs[:min(len(attrs), 64)] {
		cleaned = append(cleaned, cleanAttr(a))
	}
	return &safeHandler{Handler: h.Handler.WithAttrs(cleaned)}
}
func (h *safeHandler) WithGroup(group string) slog.Handler {
	return &safeHandler{Handler: h.Handler.WithGroup(group)}
}

type contextHandler struct {
	slog.Handler
	ctx context.Context
}

func (h contextHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.Handler.Enabled(h.ctx, level)
}
func (h contextHandler) Handle(_ context.Context, r slog.Record) error {
	return h.Handler.Handle(h.ctx, r)
}
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs), h.ctx}
}
func (h contextHandler) WithGroup(group string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(group), h.ctx}
}
