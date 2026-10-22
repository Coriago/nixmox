package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdk "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
	"nixmox/internal/domain"
)

func metricFixture() domain.HostTelemetry {
	now := time.Now().UTC()
	usage := 0.25
	cpus := 4
	uptime := 1234.5
	return domain.HostTelemetry{SchemaVersion: 1, HostID: "n_metric", ObservedAt: &now, IntervalSeconds: 0.5, CPU: domain.CPUUsage{Utilization: &usage, LogicalCount: &cpus, LoadAverage: []float64{0.1, 0.2, 0.3}}, UptimeSeconds: &uptime, Memory: &domain.MemoryUsage{TotalBytes: 1024, AvailableBytes: 768, UsedBytes: 256}, Swap: &domain.SwapUsage{TotalBytes: 256, UsedBytes: 64}, Disks: []domain.DiskUsage{{Device: "sda", ReadBytes: 4096, WriteBytes: 8192}}, Network: []domain.NetworkUsage{{Interface: "eth0", RXBytes: 12345, TXBytes: 54321}}, Filesystems: domain.FilesystemSample{ObservedAt: &now, IntervalSeconds: 10, Items: []domain.FilesystemUsage{{Mount: "/", TotalBytes: 1024, UsedBytes: 256, AvailableBytes: 640}}}}
}
func TestOTLPUsesCachedValuesAndCumulativeCounters(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=should-not-leak")
	sample := metricFixture()
	received := make(chan *collector.ExportMetricsServiceRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" || r.Method != "POST" || r.Header.Get("Authorization") != "" {
			t.Error("incorrect endpoint, method or ambient credentials")
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Error(err)
			return
		}
		var request collector.ExportMetricsServiceRequest
		if proto.Unmarshal(data, &request) != nil {
			t.Error("invalid OTLP protobuf")
			return
		}
		select {
		case received <- &request:
		default:
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	exporter, err := StartOTLP(server.URL, sample.HostID, "sample-host", 250*time.Millisecond, func() domain.HostTelemetry { return sample })
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close()
	select {
	case <-time.After(3 * time.Second):
		t.Fatal("no OTLP export")
	case request := <-received:
		if len(request.ResourceMetrics) != 1 {
			t.Fatal("unexpected resource count")
		}
		r := request.ResourceMetrics[0]
		attrs := map[string]string{}
		for _, a := range r.Resource.Attributes {
			attrs[a.Key] = a.Value.GetStringValue()
		}
		if attrs["host.id"] != sample.HostID || attrs["host.name"] != "sample-host" || r.SchemaUrl != SystemConventionSchema {
			t.Fatalf("wrong resource: %+v", r)
		}
		metrics := map[string]*metricspb.Metric{}
		for _, scope := range r.ScopeMetrics {
			for _, m := range scope.Metrics {
				metrics[m.Name] = m
			}
		}
		if metrics["nixmox.host.cpu.utilization"].GetGauge().DataPoints[0].GetAsDouble() != *sample.CPU.Utilization {
			t.Fatal("CPU value differs from UI cache")
		}
		if metrics["system.memory.usage"].GetSum().DataPoints[0].GetAsInt() != int64(sample.Memory.UsedBytes) {
			t.Fatal("memory differs from UI cache")
		}
		disk := metrics["system.disk.io"].GetSum()
		if !disk.IsMonotonic || disk.AggregationTemporality != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE || len(disk.DataPoints) != 2 {
			t.Fatal("I/O is not cumulative")
		}
		if disk.DataPoints[0].GetAsInt() != 4096 && disk.DataPoints[1].GetAsInt() != 4096 {
			t.Fatal("disk counters were replaced by rates")
		}
	}
}

func TestMetricGroupsOmitStaleValuesIndependently(t *testing.T) {
	sample := metricFixture()
	old := time.Now().Add(-4 * time.Second)
	sample.ObservedAt = &old
	reader := sdk.NewManualReader()
	provider := sdk.NewMeterProvider(sdk.WithReader(reader))
	defer provider.Shutdown(context.Background())
	if err := registerHostMetrics(provider.Meter("test"), func() domain.HostTelemetry { return sample }); err != nil {
		t.Fatal(err)
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			names[m.Name] = true
			if m.Name == "system.filesystem.usage" {
				sum := m.Data.(metricdata.Sum[int64])
				states := map[string]int64{}
				for _, point := range sum.DataPoints {
					value, _ := point.Attributes.Value(attribute.Key("system.filesystem.state"))
					states[value.AsString()] = point.Value
				}
				if states["reserved"] != 128 || states["free"] != 640 || states["used"] != 256 {
					t.Fatalf("reserved space lost: %+v", states)
				}
			}
		}
	}
	if names["system.memory.usage"] || names["system.disk.io"] || !names["system.filesystem.usage"] {
		t.Fatalf("stale groups exported or fresh filesystem omitted: %+v", names)
	}
}

func TestCollectorFailureCannotStopSamplingOrQueueExports(t *testing.T) {
	sampler := Start(func() domain.Identity { return domain.Identity{NodeID: "n_failure"} }, 250*time.Millisecond, time.Second)
	defer sampler.Close()
	var active, maxActive, requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		requests.Add(1)
		count := active.Add(1)
		defer active.Add(-1)
		for previous := maxActive.Load(); count > previous && !maxActive.CompareAndSwap(previous, count); previous = maxActive.Load() {
		}
		select {
		case <-r.Context().Done():
		case <-time.After(4 * time.Second):
		}
		w.WriteHeader(503)
	}))
	defer server.Close()
	exporter, err := StartOTLP(server.URL, "n_failure", "test", 250*time.Millisecond, sampler.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	time.Sleep(2600 * time.Millisecond)
	if sampler.Snapshot().Sequence < 8 {
		t.Fatal("collector blocked OS sampling")
	}
	if requests.Load() > 2 || exporter.Stats().Failures == 0 {
		t.Fatalf("retry queue or missing failure: requests=%d stats=%+v", requests.Load(), exporter.Stats())
	}
	exporter.Close()
	if time.Since(start) > 6*time.Second {
		t.Fatal("exporter shutdown exceeded bounded flush")
	}
	// The server can still be finishing a cancelled request, but the client has
	// only one live connection and one active collection/export call.
	if maxActive.Load() > 2 {
		t.Fatal("exports accumulated without a bound")
	}
}
