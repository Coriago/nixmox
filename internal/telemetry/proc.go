// Package telemetry samples the local OS independently of network delivery.
package telemetry

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nixmox/internal/domain"
)

type counterPair struct{ first, second uint64 }
type baseline struct {
	at             time.Time
	cpu            *counterPair
	disks, network map[string]counterPair
}
type procReader struct {
	root     string
	previous baseline
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(b) > 1<<20 {
		return nil, errors.New("measurement exceeds limit")
	}
	return b, err
}
func number(s string) (uint64, error) { return strconv.ParseUint(s, 10, 64) }
func rate(current, old uint64, elapsed float64, valid bool) *float64 {
	if !valid || elapsed <= 0 || current < old {
		return nil
	}
	v := float64(current-old) / elapsed
	return &v
}
func (p *procReader) sample(now time.Time) domain.HostTelemetry {
	observed := now.UTC()
	s := domain.HostTelemetry{SchemaVersion: 1, ObservedAt: &observed, Freshness: "live", Unavailable: []domain.MeasurementUnavailable{}}
	next := baseline{at: now, disks: map[string]counterPair{}, network: map[string]counterPair{}}
	elapsed := now.Sub(p.previous.at).Seconds()
	unavailable := func(field string) {
		s.Unavailable = append(s.Unavailable, domain.MeasurementUnavailable{Field: field, Reason: "Measurement unavailable or incomplete."})
	}
	read := func(name string) (string, bool) {
		b, err := readBounded(filepath.Join(p.root, name))
		return string(b), err == nil
	}
	if data, ok := read("stat"); ok {
		count := 0
		for line := range strings.SplitSeq(data, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			if strings.HasPrefix(f[0], "cpu") && f[0] != "cpu" {
				count++
				continue
			}
			if f[0] != "cpu" || len(f) < 6 {
				continue
			}
			var total, idle uint64
			valid := true
			// guest/guest_nice are already included in user/nice.
			for i := 1; i < min(len(f), 9); i++ {
				n, err := number(f[i])
				if err != nil {
					valid = false
					break
				}
				total += n
				if i == 4 || i == 5 {
					idle += n
				}
			}
			if valid && total >= idle {
				next.cpu = &counterPair{total, idle}
				old := p.previous.cpu
				if old != nil && total > old.first && idle >= old.second && idle-old.second <= total-old.first {
					u := 1 - float64(idle-old.second)/float64(total-old.first)
					s.CPU.Utilization = &u
				}
			}
		}
		if count > 0 {
			s.CPU.LogicalCount = &count
		}
	}
	if s.CPU.Utilization == nil {
		unavailable("cpu.utilization")
	}
	if s.CPU.LogicalCount == nil {
		unavailable("cpu.logical_count")
	}
	if data, ok := read("loadavg"); ok {
		f := strings.Fields(data)
		if len(f) >= 3 {
			values := []float64{}
			for _, raw := range f[:3] {
				value, err := strconv.ParseFloat(raw, 64)
				if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
					break
				}
				values = append(values, value)
			}
			if len(values) == 3 {
				s.CPU.LoadAverage = values
			}
		}
	}
	if s.CPU.LoadAverage == nil {
		unavailable("cpu.load_average")
	}
	if data, ok := read("meminfo"); ok {
		values := map[string]uint64{}
		for line := range strings.SplitSeq(data, "\n") {
			f := strings.Fields(line)
			if len(f) != 3 || f[2] != "kB" {
				continue
			}
			n, err := number(f[1])
			if err == nil && n <= ^uint64(0)/1024 {
				values[strings.TrimSuffix(f[0], ":")] = n * 1024
			}
		}
		total, tok := values["MemTotal"]
		available, aok := values["MemAvailable"]
		if tok && aok && available <= total {
			s.Memory = &domain.MemoryUsage{TotalBytes: total, AvailableBytes: available, UsedBytes: total - available}
		}
		total, tok = values["SwapTotal"]
		free, fok := values["SwapFree"]
		if tok && fok && free <= total {
			s.Swap = &domain.SwapUsage{TotalBytes: total, UsedBytes: total - free}
		}
	}
	if s.Memory == nil {
		unavailable("memory")
	}
	if s.Swap == nil {
		unavailable("swap")
	}
	if data, ok := read("uptime"); ok {
		f := strings.Fields(data)
		if len(f) > 0 {
			n, err := strconv.ParseFloat(f[0], 64)
			if err == nil && n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) {
				s.UptimeSeconds = &n
			}
		}
	}
	if s.UptimeSeconds == nil {
		unavailable("uptime_seconds")
	}
	if data, ok := read("diskstats"); ok {
		s.Disks = []domain.DiskUsage{}
		for line := range strings.SplitSeq(data, "\n") {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			if len(f) < 14 || len(s.Disks) >= 256 {
				unavailable("disks")
				break
			}
			r, re := number(f[5])
			w, we := number(f[9])
			if re != nil || we != nil || r > ^uint64(0)/512 || w > ^uint64(0)/512 {
				unavailable("disks." + f[2])
				continue
			}
			r *= 512
			w *= 512
			old, exists := p.previous.disks[f[2]]
			next.disks[f[2]] = counterPair{r, w}
			s.Disks = append(s.Disks, domain.DiskUsage{Device: f[2], ReadBytes: r, WriteBytes: w, ReadBytesPerSecond: rate(r, old.first, elapsed, exists), WriteBytesPerSecond: rate(w, old.second, elapsed, exists)})
			if !exists || r < old.first || w < old.second {
				unavailable("disks." + f[2] + ".rates")
			}
		}
	} else {
		unavailable("disks")
	}
	if data, ok := read("net/dev"); ok {
		s.Network = []domain.NetworkUsage{}
		for line := range strings.SplitSeq(data, "\n") {
			name, raw, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			name = strings.TrimSpace(name)
			f := strings.Fields(raw)
			if len(f) < 16 || len(s.Network) >= 256 {
				unavailable("network")
				break
			}
			r, re := number(f[0])
			w, we := number(f[8])
			if re != nil || we != nil {
				unavailable("network." + name)
				continue
			}
			old, exists := p.previous.network[name]
			next.network[name] = counterPair{r, w}
			s.Network = append(s.Network, domain.NetworkUsage{Interface: name, RXBytes: r, TXBytes: w, RXBytesPerSecond: rate(r, old.first, elapsed, exists), TXBytesPerSecond: rate(w, old.second, elapsed, exists)})
			if !exists || r < old.first || w < old.second {
				unavailable("network." + name + ".rates")
			}
		}
	} else {
		unavailable("network")
	}
	p.previous = next
	return s
}
