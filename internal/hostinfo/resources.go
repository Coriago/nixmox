package hostinfo

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const InventoryInterval = 30 * time.Second

// Resources combines independently sampled activity and inventory for API reads.
// Missing kernel files leave zero values; collection requires no subprocesses.
type Resources struct {
	SampledAt       time.Time    `json:"sampled_at"`
	InventoryAt     time.Time    `json:"inventory_at"`
	CPUCount        int          `json:"cpu_count"`
	CPUModel        string       `json:"cpu_model"`
	CPUPercent      float64      `json:"cpu_percent"`
	Load            string       `json:"load"`
	MemoryTotal     uint64       `json:"memory_total"`
	MemoryAvailable uint64       `json:"memory_available"`
	Interfaces      []Interface  `json:"interfaces"`
	Drives          []Drive      `json:"drives"`
	Filesystems     []Filesystem `json:"filesystems"`
}
type Interface struct {
	Name       string   `json:"name"`
	MAC        string   `json:"mac"`
	Status     string   `json:"status"`
	Addresses  []string `json:"addresses"`
	RXBytes    uint64   `json:"rx_bytes"`
	TXBytes    uint64   `json:"tx_bytes"`
	RXRate     float64  `json:"rx_rate"`
	TXRate     float64  `json:"tx_rate"`
	RatesReady bool     `json:"rates_ready"`
}
type Drive struct {
	Name       string   `json:"name"`
	Size       uint64   `json:"size"`
	Partition  bool     `json:"partition"`
	Mounts     []string `json:"mounts"`
	ReadBytes  uint64   `json:"read_bytes"`
	WriteBytes uint64   `json:"write_bytes"`
	ReadRate   float64  `json:"read_rate"`
	WriteRate  float64  `json:"write_rate"`
	RatesReady bool     `json:"rates_ready"`
}
type Filesystem struct {
	Mount     string `json:"mount"`
	Type      string `json:"type"`
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
	Used      uint64 `json:"used"`
}

var samples struct {
	sync.Mutex
	at          time.Time
	total, idle uint64
	value       Resources
}

func readText(path string) string { b, _ := os.ReadFile(path); return strings.TrimSpace(string(b)) }
func number(s string) uint64      { n, _ := strconv.ParseUint(s, 10, 64); return n }
func cpuTimes(s string) (total, idle uint64) {
	fields := strings.Fields(strings.SplitN(s, "\n", 2)[0])
	// guest and guest_nice are already included in user/nice.
	for i := 1; i < len(fields) && i <= 8; i++ {
		n := number(fields[i])
		total += n
		if i == 4 || i == 5 {
			idle += n
		}
	}
	return
}
func counterRate(current, previous uint64, seconds float64, known bool) (float64, bool) {
	if !known || seconds <= 0 || current < previous {
		return 0, false
	}
	return float64(current-previous) / seconds, true
}
func resources(refresh bool) Resources {
	samples.Lock()
	defer samples.Unlock()
	if !refresh && !samples.at.IsZero() {
		return samples.value
	}
	now := time.Now()
	previous := samples.value
	r := previous
	if r.InventoryAt.IsZero() || now.Sub(r.InventoryAt) >= InventoryInterval {
		r = inventory(now)
	}
	// Never mutate slices held by previous snapshots or concurrent readers.
	r.Interfaces = append([]Interface{}, r.Interfaces...)
	r.Drives = append([]Drive{}, r.Drives...)
	r.SampledAt = now
	r.Load = readText("/proc/loadavg")
	total, idle := cpuTimes(readText("/proc/stat"))
	if total > samples.total && samples.total != 0 && idle >= samples.idle && total-samples.total >= idle-samples.idle {
		r.CPUPercent = 100 * (1 - float64(idle-samples.idle)/float64(total-samples.total))
	}
	for _, line := range strings.Split(readText("/proc/meminfo"), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			r.MemoryTotal = number(f[1]) * 1024
		case "MemAvailable:":
			r.MemoryAvailable = number(f[1]) * 1024
		}
	}
	elapsed := now.Sub(samples.at).Seconds()
	oldNIC := map[string]Interface{}
	for _, v := range previous.Interfaces {
		oldNIC[v.Name] = v
	}
	for i := range r.Interfaces {
		v := &r.Interfaces[i]
		base := "/sys/class/net/" + v.Name
		v.Status = readText(base + "/operstate")
		v.RXBytes = number(readText(base + "/statistics/rx_bytes"))
		v.TXBytes = number(readText(base + "/statistics/tx_bytes"))
		old, known := oldNIC[v.Name]
		known = known && old.MAC == v.MAC
		var rx, tx bool
		v.RXRate, rx = counterRate(v.RXBytes, old.RXBytes, elapsed, known)
		v.TXRate, tx = counterRate(v.TXBytes, old.TXBytes, elapsed, known)
		v.RatesReady = rx && tx
	}
	oldDisk := map[string]Drive{}
	for _, v := range previous.Drives {
		oldDisk[v.Name] = v
	}
	for i := range r.Drives {
		v := &r.Drives[i]
		fields := strings.Fields(readText("/sys/class/block/" + v.Name + "/stat"))
		if len(fields) < 7 {
			v.RatesReady = false
			continue
		}
		// Linux block statistics report sectors in 512-byte units.
		v.ReadBytes = number(fields[2]) * 512
		v.WriteBytes = number(fields[6]) * 512
		old, known := oldDisk[v.Name]
		known = known && old.Size == v.Size
		var read, write bool
		v.ReadRate, read = counterRate(v.ReadBytes, old.ReadBytes, elapsed, known)
		v.WriteRate, write = counterRate(v.WriteBytes, old.WriteBytes, elapsed, known)
		v.RatesReady = read && write
	}
	samples.at = now
	samples.total = total
	samples.idle = idle
	samples.value = r
	return r
}
func inventory(now time.Time) Resources {
	r := Resources{InventoryAt: now, CPUCount: runtime.NumCPU(), Interfaces: []Interface{}, Drives: []Drive{}, Filesystems: []Filesystem{}}
	for _, line := range strings.Split(readText("/proc/cpuinfo"), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
			r.CPUModel = strings.TrimSpace(v)
			break
		}
	}
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		v := Interface{Name: i.Name, MAC: i.HardwareAddr.String(), Addresses: []string{}}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			v.Addresses = append(v.Addresses, a.String())
		}
		r.Interfaces = append(r.Interfaces, v)
	}
	mounts := map[string][]string{}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`)
	for _, line := range strings.Split(readText("/proc/mounts"), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		device, mount := unescape.Replace(f[0]), unescape.Replace(f[1])
		if p, e := filepath.EvalSymlinks(device); e == nil {
			device = p
		}
		mounts[filepath.Base(device)] = append(mounts[filepath.Base(device)], mount)
		// Avoid statfs calls on remote/FUSE mounts, which can block behind a network
		// outage. Capacity here describes local filesystems, not network storage.
		switch f[2] {
		case "ext2", "ext3", "ext4", "xfs", "btrfs", "vfat", "f2fs", "zfs", "bcachefs", "overlay":
			var stat syscall.Statfs_t
			if syscall.Statfs(mount, &stat) == nil && stat.Bsize > 0 {
				size := uint64(stat.Bsize)
				r.Filesystems = append(r.Filesystems, Filesystem{Mount: mount, Type: f[2], Total: stat.Blocks * size, Available: stat.Bavail * size, Used: (stat.Blocks - stat.Bfree) * size})
			}
		}
	}
	entries, _ := os.ReadDir("/sys/class/block")
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}
		base := "/sys/class/block/" + name
		_, err := os.Stat(base + "/partition")
		r.Drives = append(r.Drives, Drive{Name: name, Size: number(readText(base+"/size")) * 512, Partition: err == nil, Mounts: mounts[name]})
	}
	return r
}
