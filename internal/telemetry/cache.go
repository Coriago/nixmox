package telemetry

import (
	"context"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"math"
	"nixmox/internal/domain"
)

// Collection reads the same bounded counters used by the gateway; exporters
// never perform peer probes, store lookups or archive reads.
func (o *OTLP) RegisterCache(snapshot func() domain.CacheMetrics) error {
	m := o.provider.Meter("nixmox.cache")
	count, err := m.Int64ObservableCounter("nixmox.cache.lookup.count", metric.WithUnit("{lookup}"))
	if err != nil {
		return err
	}
	duration, err := m.Float64ObservableCounter("nixmox.cache.lookup.duration.sum", metric.WithUnit("s"))
	if err != nil {
		return err
	}
	bytes, err := m.Int64ObservableCounter("nixmox.cache.io", metric.WithUnit("By"))
	if err != nil {
		return err
	}
	peerFailures, err := m.Int64ObservableCounter("nixmox.cache.peer.failures", metric.WithUnit("{failure}"))
	if err != nil {
		return err
	}
	failures, err := m.Int64ObservableCounter("nixmox.cache.transfer.failures", metric.WithUnit("{failure}"))
	if err != nil {
		return err
	}
	cancellations, err := m.Int64ObservableCounter("nixmox.cache.cancellations", metric.WithUnit("{request}"))
	if err != nil {
		return err
	}
	active, err := m.Int64ObservableGauge("nixmox.cache.transfers.active", metric.WithUnit("{request}"))
	if err != nil {
		return err
	}
	_, err = m.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		s := snapshot()
		observe := func(instrument metric.Int64Observable, value uint64) {
			if value <= math.MaxInt64 {
				observer.ObserveInt64(instrument, int64(value))
			}
		}
		observe(count, s.Lookups)
		observe(peerFailures, s.PeerFailures)
		observe(failures, s.Failures)
		observe(cancellations, s.Cancellations)
		observer.ObserveFloat64(duration, float64(s.LookupNanoseconds)/1e9)
		observer.ObserveInt64(active, s.Active)
		observer.ObserveInt64(bytes, s.BytesExported, metric.WithAttributes(attribute.String("direction", "export")))
		observer.ObserveInt64(bytes, s.BytesReceived, metric.WithAttributes(attribute.String("direction", "receive")))
		return nil
	}, count, duration, bytes, peerFailures, failures, cancellations, active)
	return err
}
