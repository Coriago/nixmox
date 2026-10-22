package domain

import "time"

type MeasurementUnavailable struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
type CPUUsage struct {
	Utilization  *float64  `json:"utilization"`
	LogicalCount *int      `json:"logical_count"`
	LoadAverage  []float64 `json:"load_average"`
}
type MemoryUsage struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
}
type SwapUsage struct {
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}
type DiskUsage struct {
	Device              string   `json:"device"`
	ReadBytes           uint64   `json:"read_bytes"`
	WriteBytes          uint64   `json:"write_bytes"`
	ReadBytesPerSecond  *float64 `json:"read_bytes_per_second"`
	WriteBytesPerSecond *float64 `json:"write_bytes_per_second"`
}
type NetworkUsage struct {
	Interface        string   `json:"interface"`
	RXBytes          uint64   `json:"rx_bytes"`
	TXBytes          uint64   `json:"tx_bytes"`
	RXBytesPerSecond *float64 `json:"rx_bytes_per_second"`
	TXBytesPerSecond *float64 `json:"tx_bytes_per_second"`
}
type FilesystemUsage struct {
	Mount          string `json:"mount"`
	TotalBytes     uint64 `json:"total_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}
type FilesystemSample struct {
	ObservedAt      *time.Time        `json:"observed_at"`
	IntervalSeconds float64           `json:"interval_seconds"`
	Freshness       string            `json:"freshness"`
	Items           []FilesystemUsage `json:"items"`
}
type HostTelemetry struct {
	SchemaVersion   int                      `json:"schema_version"`
	HostID          string                   `json:"host_id"`
	MembershipEpoch *int                     `json:"membership_epoch"`
	StreamID        string                   `json:"stream_id"`
	Sequence        uint64                   `json:"sequence"`
	ObservedAt      *time.Time               `json:"observed_at"`
	Freshness       string                   `json:"freshness"`
	IntervalSeconds float64                  `json:"interval_seconds"`
	UptimeSeconds   *float64                 `json:"uptime_seconds"`
	CPU             CPUUsage                 `json:"cpu"`
	Memory          *MemoryUsage             `json:"memory"`
	Swap            *SwapUsage               `json:"swap"`
	Filesystems     FilesystemSample         `json:"filesystems"`
	Disks           []DiskUsage              `json:"disks"`
	Network         []NetworkUsage           `json:"network"`
	Unavailable     []MeasurementUnavailable `json:"unavailable"`
}

func UnknownTelemetry(host string) HostTelemetry {
	return HostTelemetry{SchemaVersion: 1, HostID: host, Freshness: "unknown", Filesystems: FilesystemSample{Freshness: "unknown"}, Unavailable: []MeasurementUnavailable{{Field: "sample", Reason: "No observation is available."}}}
}
func MetricFreshness(observed *time.Time, interval float64, now time.Time) string {
	if observed == nil {
		return "unknown"
	}
	threshold := max(3*time.Second, time.Duration(interval*3*float64(time.Second)))
	if now.Sub(*observed) > threshold {
		return "stale"
	}
	return "live"
}
func (s HostTelemetry) At(now time.Time) HostTelemetry {
	s.Freshness = MetricFreshness(s.ObservedAt, s.IntervalSeconds, now)
	s.Filesystems.Freshness = MetricFreshness(s.Filesystems.ObservedAt, s.Filesystems.IntervalSeconds, now)
	return s
}
