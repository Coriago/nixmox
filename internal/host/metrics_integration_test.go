package host_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	collect "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"nixmox/internal/cluster"
	"nixmox/internal/host"
	"nixmox/internal/testutil"
)

// Exercise the same OTLP producer/NATS path used by host SSE without deadlines
// on the subscription context (the bug first exposed by the VM acceptance test).
func TestHostMetricsStream(t *testing.T) {
	// --- Test setup ---
	// ------------------
	transport := cluster.Metrics{Connection: testutil.JetStream(t).Conn()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(10*time.Second, cancel)
	defer timer.Stop()
	stopped := make(chan struct{})
	go func() { defer close(stopped); host.Metrics(ctx, "example", transport) }()
	defer func() { cancel(); <-stopped }()
	samples := 0
	complete := errors.New("received two samples")

	// --- Usage ---
	// -------------
	err := transport.Follow(ctx, "example", func(data json.RawMessage) error {
		var request collect.ExportMetricsServiceRequest
		if err := protojson.Unmarshal(data, &request); err != nil {
			return err
		}
		if len(request.ResourceMetrics) != 1 {
			return errors.New("missing host resource")
		}
		resource := request.ResourceMetrics[0]
		if resource.Resource.Attributes[0].Value.GetStringValue() != "example" {
			return errors.New("wrong host identity")
		}
		found := false
		for _, metric := range resource.ScopeMetrics[0].Metrics {
			if metric.Name == "system.memory.usage" && len(metric.GetGauge().DataPoints) == 2 {
				found = true
			}
		}
		if !found {
			return errors.New("missing grouped memory observations")
		}
		samples++
		if samples == 2 {
			return complete
		}
		return nil
	})

	// --- Assertions ---
	// ------------------
	if !errors.Is(err, complete) {
		t.Fatal(err)
	}
}
