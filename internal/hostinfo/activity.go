package hostinfo

// ActivityOnly omits slowly changing inventory from the two-second wire payload.
func ActivityOnly(r Resources) Resources {
	out := Resources{SampledAt: r.SampledAt, CPUPercent: r.CPUPercent, Load: r.Load, MemoryTotal: r.MemoryTotal, MemoryAvailable: r.MemoryAvailable}
	for _, i := range r.Interfaces {
		out.Interfaces = append(out.Interfaces, Interface{Name: i.Name, Status: i.Status, RXBytes: i.RXBytes, TXBytes: i.TXBytes, RXRate: i.RXRate, TXRate: i.TXRate, RatesReady: i.RatesReady})
	}
	for _, d := range r.Drives {
		out.Drives = append(out.Drives, Drive{Name: d.Name, ReadBytes: d.ReadBytes, WriteBytes: d.WriteBytes, ReadRate: d.ReadRate, WriteRate: d.WriteRate, RatesReady: d.RatesReady})
	}
	return out
}

// MergeActivity preserves inventory until the publisher sends its next refresh.
func MergeActivity(base, activity Resources) Resources {
	out := activity
	out.CPUCount, out.CPUModel, out.InventoryAt, out.Filesystems = base.CPUCount, base.CPUModel, base.InventoryAt, base.Filesystems
	out.Interfaces = append([]Interface{}, activity.Interfaces...)
	out.Drives = append([]Drive{}, activity.Drives...)
	nics := map[string]Interface{}
	for _, i := range base.Interfaces {
		nics[i.Name] = i
	}
	for i := range out.Interfaces {
		previous := nics[out.Interfaces[i].Name]
		out.Interfaces[i].MAC = previous.MAC
		out.Interfaces[i].Addresses = previous.Addresses
	}
	disks := map[string]Drive{}
	for _, d := range base.Drives {
		disks[d.Name] = d
	}
	for i := range out.Drives {
		previous := disks[out.Drives[i].Name]
		out.Drives[i].Size = previous.Size
		out.Drives[i].Partition = previous.Partition
		out.Drives[i].Mounts = previous.Mounts
	}
	return out
}
