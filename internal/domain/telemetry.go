package domain

type HostTelemetry struct {
	SchemaVersion   int     `json:"schema_version"`
	HostID          ID      `json:"host_id"`
	MembershipEpoch *uint64 `json:"membership_epoch"`
	StreamID        ID      `json:"stream_id"`
	Sequence        uint64  `json:"sequence"`
	Observed
	IntervalSeconds float64            `json:"interval_seconds"`
	UptimeSeconds   *float64           `json:"uptime_seconds"`
	CPU             *CPU               `json:"cpu"`
	Memory          *Memory            `json:"memory"`
	Swap            *Swap              `json:"swap"`
	Filesystems     Filesystems        `json:"filesystems"`
	Disks           []DiskIO           `json:"disks"`
	Network         []NetworkIO        `json:"network"`
	Unavailable     []MeasurementError `json:"unavailable"`
}

type CPU struct {
	Utilization  *float64    `json:"utilization"`
	LogicalCount *int        `json:"logical_count"`
	LoadAverage  *[3]float64 `json:"load_average"`
}

type Memory struct {
	TotalBytes     *uint64 `json:"total_bytes"`
	AvailableBytes *uint64 `json:"available_bytes"`
	UsedBytes      *uint64 `json:"used_bytes"`
}

type Swap struct {
	TotalBytes *uint64 `json:"total_bytes"`
	UsedBytes  *uint64 `json:"used_bytes"`
}

type Filesystems struct {
	Observed
	IntervalSeconds float64      `json:"interval_seconds"`
	Items           []Filesystem `json:"items"`
}

type Filesystem struct {
	Mount          string  `json:"mount"`
	TotalBytes     *uint64 `json:"total_bytes"`
	UsedBytes      *uint64 `json:"used_bytes"`
	AvailableBytes *uint64 `json:"available_bytes"`
}

type DiskIO struct {
	Device              string   `json:"device"`
	ReadBytes           *uint64  `json:"read_bytes"`
	WriteBytes          *uint64  `json:"write_bytes"`
	ReadBytesPerSecond  *float64 `json:"read_bytes_per_second"`
	WriteBytesPerSecond *float64 `json:"write_bytes_per_second"`
}

type NetworkIO struct {
	Interface        string   `json:"interface"`
	RXBytes          *uint64  `json:"rx_bytes"`
	TXBytes          *uint64  `json:"tx_bytes"`
	RXBytesPerSecond *float64 `json:"rx_bytes_per_second"`
	TXBytesPerSecond *float64 `json:"tx_bytes_per_second"`
}

type MeasurementError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
