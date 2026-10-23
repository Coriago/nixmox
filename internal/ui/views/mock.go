package views

import (
	"fmt"
	"math"
	"nixmox/internal/ui"
	"time"

	"nixmox/internal/ui/components/confirmation"
	"nixmox/internal/ui/components/progress"
)

func (m *Model) resetMock() {
	m.elapsed = 0
	m.accumulator = 0
	m.bars = map[string]*progress.Spring{}
	m.Hosts = []Host{
		{ID: "alder", Name: "alder", Address: "192.168.10.21", Reachability: "online", Membership: "pending", Role: "Provisional authority", CPU: 0.23, Memory: "4.2 / 16 GiB"},
		{ID: "birch", Name: "birch", Address: "192.168.10.22", Reachability: "online", Membership: "pending", Role: "Non-voting leaf", CPU: 0.41, Memory: "7.1 / 32 GiB"},
		{ID: "cedar", Name: "cedar", Address: "192.168.10.23", Reachability: "offline", Membership: "pending", Role: "Non-voting leaf", Memory: "Unknown"},
	}
	for i := range m.Hosts {
		h := &m.Hosts[i]
		h.Architecture, h.OS, h.Uptime = "x86_64-linux", "NixOS 26.05", "2d 6h 14m"
		h.Processor, h.Cores, h.MemoryGiB, h.MemoryUsedGiB = "AMD Ryzen 7 5700G", 16, 16, 4.2
		if i == 1 {
			h.Processor, h.Cores, h.MemoryGiB, h.MemoryUsedGiB = "Intel Core i5-12500", 12, 32, 7.1
		}
		if i == 2 {
			h.Processor, h.Cores, h.MemoryGiB = "Intel N100", 4, 8
			h.Uptime = "Unknown"
		}
		if h.Reachability == "online" {
			for n := range 60 {
				h.History = append(h.History, .2+.17*(1+math.Sin(float64(n-59)/5+float64(i)*2)))
			}
			h.CPU = h.History[len(h.History)-1]
		}
	}
	m.Discoveries = []Discovery{
		{ID: "spruce", Name: "spruce", Address: "192.168.10.25", Fingerprint: "SHA256:82:ad:14:ff"},
		{ID: "willow", Name: "willow", Address: "192.168.10.24", Fingerprint: "SHA256:7f:91:ab:42", Requested: true},
		{ID: "maple", Name: "maple", Address: "192.168.10.26", Fingerprint: "SHA256:b1:4e:90:1c", Requested: true},
	}
	m.Tasks = []Task{
		{ID: "network-birch", Name: "Network check", HostID: "birch", State: "running", Phase: "Probing management endpoints", Total: 48, Done: 9},
		{ID: "network-alder", Name: "Network check", HostID: "alder", State: "running", Phase: "Inspecting peer connectivity", Total: 32, Done: 6},
		{ID: "power-cedar", Name: "Shutdown", HostID: "cedar", State: "reconciling", Phase: "Waiting for target observation", Result: "Shutdown scheduled. Power-off has not been verified.", Age: 42 * time.Second},
		{ID: "join-willow", Name: "Join request", HostID: "willow", State: "blocked", Phase: "Awaiting exact-key approval", Result: "No membership or broker access has been granted."},
		{ID: "network-previous", Name: "Network check", HostID: "alder", State: "succeeded", Phase: "Connectivity verified", Done: 12, Total: 12, Age: 4 * time.Second, Result: "All 12 management endpoints responded."},
		{ID: "network-birch-previous", Name: "Network check", HostID: "birch", State: "succeeded", Phase: "Connectivity verified", Done: 24, Total: 24, Age: 6 * time.Second, Result: "All 24 management endpoints responded."},
		{ID: "refresh-birch", Name: "Refresh inventory", HostID: "birch", State: "succeeded", Phase: "Observations recorded", Age: time.Second, Result: "Mock inventory snapshot recorded."},
		{ID: "network-birch-failed", Name: "Network check", HostID: "birch", State: "failed", Phase: "Preferred route unavailable", Age: 12 * time.Second, Result: "Mock preferred management interface was unavailable. No fallback permitted; no configuration changed."},
		{ID: "refresh-cedar", Name: "Refresh inventory", HostID: "cedar", State: "succeeded", Phase: "Historical snapshot recorded", Age: time.Second, Result: "Historical observation only. This does not prove current reachability."},
	}
	for i := range m.Tasks {
		t := &m.Tasks[i]
		stamp := 14*3600 + 32*60 + 30 - i*30
		t.Started = fmt.Sprintf("%02d:%02d:%02d", stamp/3600, stamp/60%60, stamp%60)
		t.Log.Append("14:32:00  INFO  mock executor ready · " + t.HostID)
		t.Log.Append("14:32:01  INFO  " + t.Phase)
		if t.Total > 0 {
			m.bars[t.ID] = &progress.Spring{Position: float64(t.Done) / float64(t.Total)}
		}
	}
}

var phases = []string{"Reading interface observations", "Resolving management endpoints", "Verifying peer connectivity", "Recording connectivity evidence"}

func (m *Model) advanceMock() {
	// One observation per simulated second. Render frames never fabricate events.
	now := int(m.elapsed / time.Second)
	previous := int((m.elapsed - progress.FrameInterval) / time.Second)
	if now == previous {
		return
	}
	for i := range m.Tasks {
		t := &m.Tasks[i]
		if !ui.Active(t.State) {
			continue
		}
		t.Age += time.Second
		if t.State == "running" {
			t.Done = min(t.Total, t.Done+1)
			t.Phase = phases[min(3, t.Done*4/max(1, t.Total))]
			t.Log.Append(fmt.Sprintf("14:%02d:%02d  INFO  endpoint probe %02d/%02d · response verified", 32+now/60, now%60, t.Done, t.Total))
			if t.Done == t.Total {
				t.State = "succeeded"
				t.Phase = "Connectivity verified"
				t.Result = fmt.Sprintf("All %d management endpoints responded.", t.Total)
				t.Log.Append("INFO  completed · persisted mock result")
			}
		} else if now%4 == 0 {
			t.Log.Append("WARN  target unavailable · retaining original receipt; no power replay")
		}
	}
	for i := range m.Hosts {
		h := &m.Hosts[i]
		if h.Reachability != "online" {
			continue
		}
		h.CPU = 0.2 + 0.17*(1+math.Sin(float64(now)/5+float64(i)*2))
		h.History = append(h.History, h.CPU)
		if len(h.History) > 60 {
			h.History = h.History[1:]
		}
	}
}
func (m *Model) confirmMock(c confirmation.Model) {
	switch c.Action {
	case "shutdown":
		if h := m.Host(m.HostID); h != nil {
			t := Task{ID: fmt.Sprintf("mock-shutdown-%d", len(m.Tasks)), Name: "Shutdown", HostID: h.ID, State: "reconciling", Phase: "Waiting for target observation", Result: "Simulated scheduling only. Power-off has not been observed."}
			t.Log.Append("INFO  mock schedule recorded · no real power command")
			t.Started = m.mockClock()
			m.Tasks = append(m.Tasks, t)
			m.TaskID = t.ID
			m.HostID = ""
			m.Page = "Tasks"
			m.Tab = "Overview"
		}
	case "cancel":
		if t := m.Task(m.TaskID); t != nil && t.State == "running" {
			t.State = "cancelled"
			t.Phase = "Cancelled by operator"
			t.Result = "Mock execution cancelled; no external effects."
			t.Log.Append("WARN  cancelled by operator")
		}
	case "approve", "reject", "add":
		m.decideDiscovery(c)
		return
	}
	m.Notice = "Preview only · no commands submitted to real hosts."
}
