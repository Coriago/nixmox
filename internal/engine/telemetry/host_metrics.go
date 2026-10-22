package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

// ObserveHost exports only the local sampler cache. The UI schema is independent
// of the pinned semantic-convention mapping; no callback performs OS I/O.
func (t *OTel) ObserveHost(sampler engine.HostSampler) (metric.Registration, error) {
	meter := t.Metrics.Meter("nixmox.host", metric.WithSchemaURL("https://opentelemetry.io/schemas/1.37.0"))
	uptime, _ := meter.Float64ObservableGauge("system.uptime", metric.WithUnit("s"))
	utilization, _ := meter.Float64ObservableGauge("nixmox.host.cpu.utilization", metric.WithUnit("1")) // aggregate busy fraction, not per-CPU mode
	memory, _ := meter.Float64ObservableUpDownCounter("system.memory.usage", metric.WithUnit("By"))
	available, _ := meter.Float64ObservableUpDownCounter("system.memory.linux.available", metric.WithUnit("By"))
	memoryLimit, _ := meter.Float64ObservableUpDownCounter("system.memory.limit", metric.WithUnit("By"))
	paging, _ := meter.Float64ObservableUpDownCounter("system.paging.usage", metric.WithUnit("By"))
	filesystem, _ := meter.Float64ObservableUpDownCounter("system.filesystem.usage", metric.WithUnit("By"))
	disk, _ := meter.Float64ObservableCounter("system.disk.io", metric.WithUnit("By"))
	network, _ := meter.Float64ObservableCounter("system.network.io", metric.WithUnit("By"))
	return meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		sample := sampler.Snapshot(ctx)
		if sample.Freshness == domain.Live {
			if sample.UptimeSeconds != nil {
				o.ObserveFloat64(uptime, *sample.UptimeSeconds)
			}
			if sample.CPU != nil && sample.CPU.Utilization != nil {
				o.ObserveFloat64(utilization, *sample.CPU.Utilization)
			}
			if m := sample.Memory; m != nil {
				if m.UsedBytes != nil {
					o.ObserveFloat64(memory, float64(*m.UsedBytes), metric.WithAttributes(attribute.String("system.memory.state", "used")))
				}
				if m.AvailableBytes != nil {
					o.ObserveFloat64(available, float64(*m.AvailableBytes))
				}
				if m.TotalBytes != nil {
					o.ObserveFloat64(memoryLimit, float64(*m.TotalBytes))
				}
			}
			if m := sample.Swap; m != nil && m.UsedBytes != nil {
				o.ObserveFloat64(paging, float64(*m.UsedBytes), metric.WithAttributes(attribute.String("system.paging.state", "used")))
			}
			for _, d := range sample.Disks {
				for direction, value := range map[string]*uint64{"read": d.ReadBytes, "write": d.WriteBytes} {
					if value != nil {
						o.ObserveFloat64(disk, float64(*value), metric.WithAttributes(attribute.String("system.device", d.Device), attribute.String("disk.io.direction", direction)))
					}
				}
			}
			for _, n := range sample.Network {
				for direction, value := range map[string]*uint64{"receive": n.RXBytes, "transmit": n.TXBytes} {
					if value != nil {
						o.ObserveFloat64(network, float64(*value), metric.WithAttributes(attribute.String("system.device", n.Interface), attribute.String("network.io.direction", direction)))
					}
				}
			}
		}
		if sample.Filesystems.Freshness == domain.Live {
			for _, f := range sample.Filesystems.Items {
				if f.UsedBytes != nil {
					o.ObserveFloat64(filesystem, float64(*f.UsedBytes), metric.WithAttributes(attribute.String("system.filesystem.mountpoint", f.Mount), attribute.String("system.filesystem.state", "used")))
				}
			}
		}
		return nil
	}, uptime, utilization, memory, available, memoryLimit, paging, filesystem, disk, network)
}
