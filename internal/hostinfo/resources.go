package hostinfo

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Resources is a best-effort Linux inventory. Missing kernel files leave zero
// values; collection never requires root or launches subprocesses.
type Resources struct {
	CPUCount        int         `json:"cpu_count"`
	CPUModel        string      `json:"cpu_model"`
	CPUPercent      float64     `json:"cpu_percent"`
	Load            string      `json:"load"`
	MemoryTotal     uint64      `json:"memory_total"`
	MemoryAvailable uint64      `json:"memory_available"`
	Interfaces      []Interface `json:"interfaces"`
	Drives          []Drive     `json:"drives"`
}
type Interface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	Status    string   `json:"status"`
	Addresses []string `json:"addresses"`
	RXBytes   uint64   `json:"rx_bytes"`
	TXBytes   uint64   `json:"tx_bytes"`
}
type Drive struct {
	Name      string   `json:"name"`
	Size      uint64   `json:"size"`
	Partition bool     `json:"partition"`
	Mounts    []string `json:"mounts"`
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
func resources() Resources {
	samples.Lock()
	defer samples.Unlock()
	if time.Since(samples.at) < 2*time.Second {
		return samples.value
	}
	r := Resources{CPUCount: runtime.NumCPU(), Load: readText("/proc/loadavg"), Interfaces: []Interface{}, Drives: []Drive{}}
	total, idle := cpuTimes(readText("/proc/stat"))
	if total > samples.total && samples.total != 0 && idle >= samples.idle && total-samples.total >= idle-samples.idle {
		r.CPUPercent = 100 * (1 - float64(idle-samples.idle)/float64(total-samples.total))
	}
	for _, line := range strings.Split(readText("/proc/cpuinfo"), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
			r.CPUModel = strings.TrimSpace(v)
			break
		}
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
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		addrs, _ := i.Addrs()
		v := Interface{Name: i.Name, MAC: i.HardwareAddr.String(), Status: readText("/sys/class/net/" + i.Name + "/operstate"), Addresses: []string{}}
		for _, a := range addrs {
			v.Addresses = append(v.Addresses, a.String())
		}
		v.RXBytes = number(readText("/sys/class/net/" + i.Name + "/statistics/rx_bytes"))
		v.TXBytes = number(readText("/sys/class/net/" + i.Name + "/statistics/tx_bytes"))
		r.Interfaces = append(r.Interfaces, v)
	}
	mounts := map[string][]string{}
	for _, line := range strings.Split(readText("/proc/mounts"), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 {
			device := f[0]
			if p, e := filepath.EvalSymlinks(device); e == nil {
				device = p
			}
			mounts[filepath.Base(device)] = append(mounts[filepath.Base(device)], f[1])
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
	samples.at = time.Now()
	samples.total = total
	samples.idle = idle
	samples.value = r
	return r
}
