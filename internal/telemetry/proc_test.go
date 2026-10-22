package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nixmox/internal/domain"
)

func TestCounterRatesAndMissingMeasurements(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", "cpu 100 0 50 800 50 0 0 0 10 0\ncpu0 0\ncpu1 0\n")
	write("loadavg", "0.5 0.4 0.3 1/10 20\n")
	write("uptime", "120 220\n")
	write("meminfo", "MemTotal: 1024 kB\nMemAvailable: 256 kB\nSwapTotal: 512 kB\nSwapFree: 128 kB\n")
	write("diskstats", "8 0 sda 1 0 20 0 2 0 40 0 0 0 0\n")
	write("net/dev", "eth0: 100 1 0 0 0 0 0 0 200 1 0 0 0 0 0 0\n")
	p := procReader{root: dir}
	now := time.Now()
	first := p.sample(now)
	if first.CPU.Utilization != nil || first.Network[0].RXBytesPerSecond != nil || first.Disks[0].ReadBytesPerSecond != nil {
		t.Fatal("first sample fabricated rates")
	}
	if first.Memory.UsedBytes != 768*1024 || first.Swap.UsedBytes != 384*1024 || *first.CPU.LogicalCount != 2 {
		t.Fatalf("incorrect measurements: %+v", first)
	}
	write("stat", "cpu 150 0 50 820 80 0 0 0 20 0\ncpu0 0\ncpu1 0\n")
	write("net/dev", "eth0: 300 1 0 0 0 0 0 0 600 1 0 0 0 0 0 0\n")
	write("diskstats", "8 0 sda 1 0 24 0 2 0 44 0 0 0 0\n")
	second := p.sample(now.Add(2 * time.Second))
	if *second.CPU.Utilization != 0.5 || *second.Network[0].RXBytesPerSecond != 100 || *second.Disks[0].ReadBytesPerSecond != 1024 {
		t.Fatalf("wrong rates: %+v", second)
	}
	write("net/dev", "eth0: 1 1 0 0 0 0 0 0 2 1 0 0 0 0 0 0\n")
	if err := os.Remove(filepath.Join(dir, "meminfo")); err != nil {
		t.Fatal(err)
	}
	third := p.sample(now.Add(3 * time.Second))
	if third.Network[0].RXBytesPerSecond != nil || third.Memory != nil || third.Swap != nil {
		t.Fatal("reset or missing measurements fabricated")
	}
	write("net/dev", "")
	p.sample(now.Add(4 * time.Second))
	write("net/dev", "eth0: 50 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0\n")
	if p.sample(now.Add(5 * time.Second)).Network[0].RXBytesPerSecond != nil {
		t.Fatal("hotplug retained retired baseline")
	}
}

func TestIndependentFreshness(t *testing.T) {
	now := time.Now()
	old := now.Add(-31 * time.Second)
	s := domain.HostTelemetry{ObservedAt: &now, IntervalSeconds: 0.5, Filesystems: domain.FilesystemSample{ObservedAt: &old, IntervalSeconds: 10}}
	if got := s.At(now); got.Freshness != "live" || got.Filesystems.Freshness != "stale" {
		t.Fatalf("freshness coupled: %+v", got)
	}
	if got := s.At(now.Add(4 * time.Second)); got.Freshness != "stale" {
		t.Fatal("fast measurements stayed live")
	}
}

func TestActualLinuxSampler(t *testing.T) {
	id := domain.Identity{NodeID: "n_test"}
	s := Start(func() domain.Identity { return id }, 250*time.Millisecond, time.Second)
	defer s.Close()
	ch, cancel := s.Subscribe()
	defer cancel()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("OS sample unavailable")
		case <-ch:
		}
		v := s.Snapshot()
		if v.Sequence >= 2 && v.Filesystems.ObservedAt != nil {
			if v.Memory == nil || v.CPU.LogicalCount == nil || v.CPU.Utilization == nil || v.UptimeSeconds == nil || v.MembershipEpoch != nil || v.Freshness != "live" {
				t.Fatalf("missing OS data: %+v", v)
			}
			return
		}
	}
}
