package host

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	systemhost "github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	network "github.com/shirou/gopsutil/v4/net"
	collect "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"nixmox/internal/cluster"
	"nixmox/internal/util"
)

func attribute(key, value string) *common.KeyValue {
	return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: value}}}
}

// Metrics publishes actual host counters in the standard OTLP protobuf schema.
// Cumulative CPU/disk/network counters permit clients to derive rates.
func Metrics(ctx context.Context, id string, transport cluster.Metrics) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		data, err := sampleMetrics(ctx, id)
		if err == nil {
			err = transport.Publish(ctx, id, data)
		}
		if err != nil && ctx.Err() == nil {
			util.From(ctx).Error("host metrics", "error", util.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func sampleMetrics(ctx context.Context, id string) ([]byte, error) {
	now := uint64(time.Now().UnixNano())
	values := []*metrics.Metric{}
	grouped := map[string]*metrics.Metric{}
	boot, err := systemhost.BootTimeWithContext(ctx)
	if err != nil {
		return nil, err
	}
	add := func(name, unit string, value float64, counter bool, attrs ...*common.KeyValue) {
		point := &metrics.NumberDataPoint{TimeUnixNano: now, Attributes: attrs, Value: &metrics.NumberDataPoint_AsDouble{AsDouble: value}}
		if counter {
			point.StartTimeUnixNano = boot * uint64(time.Second)
		}
		if metric, ok := grouped[name]; ok {
			if counter {
				metric.GetSum().DataPoints = append(metric.GetSum().DataPoints, point)
			} else {
				metric.GetGauge().DataPoints = append(metric.GetGauge().DataPoints, point)
			}
			return
		}
		metric := &metrics.Metric{Name: name, Unit: unit}
		grouped[name] = metric
		if counter {
			metric.Data = &metrics.Metric_Sum{Sum: &metrics.Sum{AggregationTemporality: metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, IsMonotonic: true, DataPoints: []*metrics.NumberDataPoint{point}}}
		} else {
			metric.Data = &metrics.Metric_Gauge{Gauge: &metrics.Gauge{DataPoints: []*metrics.NumberDataPoint{point}}}
		}
		values = append(values, metric)
	}
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return nil, err
	}
	add("system.memory.usage", "By", float64(memory.Used), false, attribute("system.memory.state", "used"))
	add("system.memory.usage", "By", float64(memory.Available), false, attribute("system.memory.state", "available"))
	times, err := cpu.TimesWithContext(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, value := range times {
		for state, seconds := range map[string]float64{"user": value.User, "system": value.System, "idle": value.Idle} {
			add("system.cpu.time", "s", seconds, true, attribute("system.cpu.state", state))
		}
	}
	io, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		return nil, err
	}
	for device, value := range io {
		for direction, bytes := range map[string]uint64{"read": value.ReadBytes, "write": value.WriteBytes} {
			add("system.disk.io", "By", float64(bytes), true, attribute("device", device), attribute("disk.io.direction", direction))
		}
		add("system.disk.operations", "{operation}", float64(value.ReadCount), true, attribute("device", device), attribute("disk.io.direction", "read"))
		add("system.disk.operations", "{operation}", float64(value.WriteCount), true, attribute("device", device), attribute("disk.io.direction", "write"))
	}
	interfaces, err := network.IOCountersWithContext(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, value := range interfaces {
		add("system.network.io", "By", float64(value.BytesRecv), true, attribute("device", value.Name), attribute("network.io.direction", "receive"))
		add("system.network.io", "By", float64(value.BytesSent), true, attribute("device", value.Name), attribute("network.io.direction", "transmit"))
	}
	return protojson.Marshal(&collect.ExportMetricsServiceRequest{ResourceMetrics: []*metrics.ResourceMetrics{{
		Resource:     &resource.Resource{Attributes: []*common.KeyValue{attribute("host.id", id)}},
		ScopeMetrics: []*metrics.ScopeMetrics{{Scope: &common.InstrumentationScope{Name: "nixmox.host"}, Metrics: values}},
	}}})
}
