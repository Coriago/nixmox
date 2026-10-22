package presentation

import (
	"fmt"
	"strings"

	"nixmox/internal/domain"
)

type MetricRow struct{ Name, Value, Freshness string }

func formatBytes(value uint64) string {
	unit := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	n := float64(value)
	i := 0
	for n >= 1024 && i < len(unit)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", n, unit[i])
}
func TelemetryRows(s domain.HostTelemetry) []MetricRow {
	rows := []MetricRow{}
	add := func(name, value string) {
		rows = append(rows, MetricRow{Name: name, Value: value, Freshness: s.Freshness})
	}
	rate := func(v *float64) string {
		if v == nil {
			return "unknown"
		}
		return formatBytes(uint64(max(0, *v))) + "/s"
	}
	cpu := "unknown"
	if s.CPU.Utilization != nil {
		cpu = fmt.Sprintf("%.1f%%", *s.CPU.Utilization*100)
	}
	add("CPU usage", cpu)
	load := "unknown"
	if len(s.CPU.LoadAverage) == 3 {
		load = fmt.Sprintf("%.2f / %.2f / %.2f", s.CPU.LoadAverage[0], s.CPU.LoadAverage[1], s.CPU.LoadAverage[2])
	}
	add("Load (1/5/15 min)", load)
	memory := "unknown"
	if s.Memory != nil {
		memory = formatBytes(s.Memory.UsedBytes) + " used / " + formatBytes(s.Memory.TotalBytes) + " total; " + formatBytes(s.Memory.AvailableBytes) + " available"
	}
	add("Memory", memory)
	swap := "unknown"
	if s.Swap != nil {
		swap = formatBytes(s.Swap.UsedBytes) + " used / " + formatBytes(s.Swap.TotalBytes) + " total"
	}
	add("Swap", swap)
	uptime := "unknown"
	if s.UptimeSeconds != nil {
		uptime = fmt.Sprintf("%.0f seconds", *s.UptimeSeconds)
	}
	add("Uptime", uptime)
	if s.Filesystems.Items == nil {
		rows = append(rows, MetricRow{Name: "Filesystems", Value: "unknown", Freshness: s.Filesystems.Freshness})
	}
	for _, fs := range s.Filesystems.Items {
		rows = append(rows, MetricRow{Name: "Filesystem " + fs.Mount, Value: formatBytes(fs.UsedBytes) + " used / " + formatBytes(fs.TotalBytes) + " total; " + formatBytes(fs.AvailableBytes) + " available", Freshness: s.Filesystems.Freshness})
	}
	if s.Disks == nil {
		add("Disk I/O", "unknown")
	}
	for _, disk := range s.Disks {
		add("Disk "+disk.Device, "read "+rate(disk.ReadBytesPerSecond)+"; write "+rate(disk.WriteBytesPerSecond))
	}
	if s.Network == nil {
		add("Network", "unknown")
	}
	for _, network := range s.Network {
		add("Network "+network.Interface, "receive "+rate(network.RXBytesPerSecond)+"; send "+rate(network.TXBytesPerSecond))
	}
	return rows
}
func TelemetryText(s domain.HostTelemetry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Host %s · %s\n", s.HostID, s.Freshness)
	if s.ObservedAt != nil {
		fmt.Fprintf(&b, "Observed %s\n", s.ObservedAt.UTC().Format("2006-01-02 15:04:05.000 UTC"))
	}
	for _, row := range TelemetryRows(s) {
		fmt.Fprintf(&b, "%s: %s [%s]\n", row.Name, row.Value, row.Freshness)
	}
	for _, issue := range s.Unavailable {
		fmt.Fprintf(&b, "Unavailable %s: %s\n", issue.Field, issue.Reason)
	}
	return b.String()
}
