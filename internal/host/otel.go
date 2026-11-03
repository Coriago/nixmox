package host

import (
	"context"

	otelattr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ObserveMetrics exposes the existing host sampler through standard SDK
// instruments. The configured SDK reader/exporter owns scheduling and transport.
func ObserveMetrics(meter metric.Meter) (metric.Registration, error) {
	instruments := map[string]metric.Float64Observable{}
	var observables []metric.Observable
	for _, definition := range []struct {
		name, unit string
		counter    bool
	}{
		{"system.memory.usage", "By", false},
		{"system.cpu.time", "s", true},
		{"system.disk.io", "By", true},
		{"system.disk.operations", "{operation}", true},
		{"system.network.io", "By", true},
	} {
		var instrument metric.Float64Observable
		var err error
		if definition.counter {
			instrument, err = meter.Float64ObservableCounter(definition.name, metric.WithUnit(definition.unit))
		} else {
			instrument, err = meter.Float64ObservableGauge(definition.name, metric.WithUnit(definition.unit))
		}
		if err != nil {
			return nil, err
		}
		instruments[definition.name] = instrument
		observables = append(observables, instrument)
	}
	return meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		values, err := collectMetrics(ctx)
		if err != nil {
			return err
		}
		for _, value := range values {
			points := value.GetGauge().GetDataPoints()
			if value.GetSum() != nil {
				points = value.GetSum().DataPoints
			}
			for _, point := range points {
				attributes := make([]otelattr.KeyValue, 0, len(point.Attributes))
				for _, label := range point.Attributes {
					attributes = append(attributes, otelattr.String(label.Key, label.Value.GetStringValue()))
				}
				observer.ObserveFloat64(instruments[value.Name], point.GetAsDouble(), metric.WithAttributes(attributes...))
			}
		}
		return nil
	}, observables...)
}
