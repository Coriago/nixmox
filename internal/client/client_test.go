package client_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"nixmox/internal/client"
	"nixmox/internal/cluster"
	"nixmox/internal/testutil"
)

func TestTraceAcrossHTTPAndNATS(t *testing.T) {
	// --- Test setup ---
	// ------------------
	previousProvider, previousPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
		provider.Shutdown(context.Background())
	}()
	js := testutil.JetStream(t)
	nc := js.Conn()
	received := make(chan trace.SpanContext, 1)
	subscription, err := nc.Subscribe("host.cedar.command", func(msg *nats.Msg) {
		ctx := cluster.Extract(context.Background(), msg)
		_, span := otel.Tracer("test").Start(ctx, "task.build", trace.WithSpanKind(trace.SpanKindConsumer))
		defer span.End()
		received <- span.SpanContext()
		_ = msg.Respond([]byte(`{"accepted":true}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Unsubscribe()
	if err = nc.Flush(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := cluster.Request(r.Context(), nc, "host.cedar.command", nil)
		if err != nil {
			http.Error(w, "dispatch failed", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(result.Data)
	}), "api.build"))
	defer server.Close()
	api, err := client.New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	// --- Usage ---
	// -------------
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx, origin := otel.Tracer("test").Start(ctx, "cli.build")
	defer origin.End()
	var result struct {
		Accepted bool `json:"accepted"`
	}
	if err = api.Do(ctx, http.MethodPost, "/hosts/cedar/builds", map[string]string{"installable": "example"}, &result); err != nil {
		t.Fatal(err)
	}
	consumer := <-received

	// --- Assertions ---
	// ------------------
	if !result.Accepted || !consumer.IsValid() || consumer.TraceID() != origin.SpanContext().TraceID() || consumer.SpanID() == origin.SpanContext().SpanID() {
		t.Fatal("trace continuity lost across CLI, HTTP, NATS, and task phase")
	}
}

func TestSSEReconnect(t *testing.T) {
	// --- Test setup ---
	// ------------------
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if call == 1 {
			fmt.Fprint(w, "retry: 250\nid: 1\nevent: task\ndata: {\"status\":\"running\"}\n\n")
			return
		}
		if r.Header.Get("Last-Event-ID") != "1" {
			t.Error("reconnect did not carry acknowledged cursor")
		}
		fmt.Fprint(w, "id: 2\nevent: task\ndata: {\"status\":\"succeeded\"}\n\n")
	}))
	defer server.Close()
	api, err := client.New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	complete := errors.New("finished")
	var ids []string

	// --- Usage ---
	// -------------
	err = api.Watch(ctx, "/tasks/build1/events", "", func(event client.Event) error {
		ids = append(ids, event.ID)
		if event.ID == "2" {
			return complete
		}
		return nil
	})

	// --- Assertions ---
	// ------------------
	if !errors.Is(err, complete) || len(ids) != 2 || ids[0] != "1" || ids[1] != "2" || calls.Load() != 2 {
		t.Fatalf("unexpected SSE delivery: %v, %v", ids, err)
	}
}
