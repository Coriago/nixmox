package telemetry

import (
	"context"
	"crypto/tls"
	"errors"
	"math"
	"net"
	"net/http"
	"net/url"
	"path"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdk "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	"nixmox/internal/domain"
)

// Pin the mapping independently of the SDK. CPU busy utilization is normalized
// across all cores in our cache; the convention's per-logical-CPU metric would
// misrepresent that value, so only that aggregate and load averages are custom.
const SystemConventionSchema = "https://opentelemetry.io/schemas/1.44.0"

type OTLP struct {
	reader    *sdk.ManualReader
	provider  *sdk.MeterProvider
	exporter  *otlpmetrichttp.Exporter
	transport *http.Transport
	cancel    context.CancelFunc
	done      chan struct{}
	exports   atomic.Uint64
	failures  atomic.Uint64
}
type ExportStats struct {
	Exports  uint64 `json:"exports"`
	Failures uint64 `json:"failures"`
}

func (o *OTLP) Stats() ExportStats {
	return ExportStats{Exports: o.exports.Load(), Failures: o.failures.Load()}
}

func StartOTLP(endpoint, hostID, hostName string, interval time.Duration, snapshot func() domain.HostTelemetry) (*OTLP, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || interval < 250*time.Millisecond || interval > time.Minute {
		return nil, errors.New("invalid OTLP configuration")
	}
	u.Path = path.Join(u.Path, "v1/metrics")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: 2 * time.Second, MaxConnsPerHost: 1, MaxIdleConns: 1, IdleConnTimeout: 30 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	exporter, err := otlpmetrichttp.New(context.Background(), otlpmetrichttp.WithEndpointURL(u.String()), otlpmetrichttp.WithHTTPClient(client), otlpmetrichttp.WithHeaders(map[string]string{}), otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}), otlpmetrichttp.WithTimeout(2*time.Second), otlpmetrichttp.WithMaxRequestSize(1<<20), otlpmetrichttp.WithMaxResponseSize(64<<10), otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression), otlpmetrichttp.WithTemporalitySelector(sdk.DefaultTemporalitySelector))
	if err != nil {
		transport.CloseIdleConnections()
		return nil, errors.New("OTLP exporter initialization failed")
	}
	reader := sdk.NewManualReader(sdk.WithTemporalitySelector(sdk.DefaultTemporalitySelector))
	provider := sdk.NewMeterProvider(sdk.WithReader(reader), sdk.WithCardinalityLimit(2048), sdk.WithResource(resource.NewWithAttributes(SystemConventionSchema, attribute.String("host.id", hostID), attribute.String("host.name", hostName), attribute.String("service.name", "nixmox"))))
	if err = registerHostMetrics(provider.Meter("nixmox.host", metric.WithSchemaURL(SystemConventionSchema)), snapshot); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = provider.Shutdown(ctx)
		_ = exporter.Shutdown(ctx)
		transport.CloseIdleConnections()
		return nil, errors.New("host metrics registration failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := &OTLP{reader: reader, provider: provider, exporter: exporter, transport: transport, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(o.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			o.export(ctx)
			// Drop ticks that occurred during a slow export; never queue snapshots.
			select {
			case <-ticker.C:
			default:
			}
		}
	}()
	return o, nil
}
func (o *OTLP) export(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	var data metricdata.ResourceMetrics
	if o.reader.Collect(ctx, &data) != nil || o.exporter.Export(ctx, &data) != nil {
		o.failures.Add(1)
		return
	}
	o.exports.Add(1)
}
func (o *OTLP) Close() {
	o.cancel()
	<-o.done
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	o.export(ctx)
	_ = o.provider.Shutdown(ctx)
	_ = o.exporter.Shutdown(ctx)
	o.transport.CloseIdleConnections()
}

func registerHostMetrics(m metric.Meter, snapshot func() domain.HostTelemetry) error {
	type definition struct{ name, unit, kind string }
	definitions := []definition{
		{"nixmox.host.cpu.utilization", "1", "gauge"}, {"nixmox.host.load_average", "{thread}", "gauge"}, {"system.uptime", "s", "gauge"},
		{"system.cpu.logical.count", "{cpu}", "updown"}, {"system.memory.usage", "By", "updown"}, {"system.memory.limit", "By", "updown"}, {"system.memory.linux.available", "By", "updown"},
		{"system.paging.usage", "By", "updown"}, {"system.filesystem.usage", "By", "updown"}, {"system.filesystem.limit", "By", "updown"},
		{"system.disk.io", "By", "counter"}, {"system.network.io", "By", "counter"},
	}
	floats := map[string]metric.Float64ObservableGauge{}
	integers := map[string]metric.Int64Observable{}
	observables := []metric.Observable{}
	for _, d := range definitions {
		if d.kind == "gauge" {
			v, err := m.Float64ObservableGauge(d.name, metric.WithUnit(d.unit))
			if err != nil {
				return err
			}
			floats[d.name] = v
			observables = append(observables, v)
			continue
		}
		var v metric.Int64Observable
		var err error
		if d.kind == "counter" {
			v, err = m.Int64ObservableCounter(d.name, metric.WithUnit(d.unit))
		} else {
			v, err = m.Int64ObservableUpDownCounter(d.name, metric.WithUnit(d.unit))
		}
		if err != nil {
			return err
		}
		integers[d.name] = v
		observables = append(observables, v)
	}
	_, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		s := snapshot().At(time.Now())
		integer := func(name string, value uint64, attrs ...attribute.KeyValue) {
			if value <= math.MaxInt64 {
				o.ObserveInt64(integers[name], int64(value), metric.WithAttributes(attrs...))
			}
		}
		floating := func(name string, value float64, attrs ...attribute.KeyValue) {
			if !math.IsNaN(value) && !math.IsInf(value, 0) {
				o.ObserveFloat64(floats[name], value, metric.WithAttributes(attrs...))
			}
		}
		if s.Freshness == "live" {
			if s.CPU.Utilization != nil {
				floating("nixmox.host.cpu.utilization", *s.CPU.Utilization)
			}
			if s.CPU.LogicalCount != nil {
				integer("system.cpu.logical.count", uint64(*s.CPU.LogicalCount))
			}
			if len(s.CPU.LoadAverage) == 3 {
				for i, window := range []string{"1m", "5m", "15m"} {
					floating("nixmox.host.load_average", s.CPU.LoadAverage[i], attribute.String("window", window))
				}
			}
			if s.UptimeSeconds != nil {
				floating("system.uptime", *s.UptimeSeconds)
			}
			if s.Memory != nil {
				integer("system.memory.usage", s.Memory.UsedBytes, attribute.String("system.memory.state", "used"))
				integer("system.memory.limit", s.Memory.TotalBytes)
				integer("system.memory.linux.available", s.Memory.AvailableBytes)
			}
			if s.Swap != nil {
				integer("system.paging.usage", s.Swap.UsedBytes, attribute.String("system.paging.state", "used"))
				if s.Swap.TotalBytes >= s.Swap.UsedBytes {
					integer("system.paging.usage", s.Swap.TotalBytes-s.Swap.UsedBytes, attribute.String("system.paging.state", "free"))
				}
			}
			for _, d := range s.Disks {
				integer("system.disk.io", d.ReadBytes, attribute.String("system.device", d.Device), attribute.String("disk.io.direction", "read"))
				integer("system.disk.io", d.WriteBytes, attribute.String("system.device", d.Device), attribute.String("disk.io.direction", "write"))
			}
			for _, n := range s.Network {
				integer("system.network.io", n.RXBytes, attribute.String("network.interface.name", n.Interface), attribute.String("network.io.direction", "receive"))
				integer("system.network.io", n.TXBytes, attribute.String("network.interface.name", n.Interface), attribute.String("network.io.direction", "transmit"))
			}
		}
		if s.Filesystems.Freshness == "live" {
			for _, fs := range s.Filesystems.Items {
				mount := attribute.String("system.filesystem.mountpoint", fs.Mount)
				integer("system.filesystem.limit", fs.TotalBytes, mount)
				integer("system.filesystem.usage", fs.UsedBytes, mount, attribute.String("system.filesystem.state", "used"))
				integer("system.filesystem.usage", fs.AvailableBytes, mount, attribute.String("system.filesystem.state", "free"))
				if fs.TotalBytes >= fs.UsedBytes && fs.TotalBytes-fs.UsedBytes >= fs.AvailableBytes {
					integer("system.filesystem.usage", fs.TotalBytes-fs.UsedBytes-fs.AvailableBytes, mount, attribute.String("system.filesystem.state", "reserved"))
				}
			}
		}
		return nil
	}, observables...)
	return err
}
