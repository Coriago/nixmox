package views

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"nixmox/internal/client"
	"nixmox/internal/domain"
	"nixmox/internal/ui"
	"nixmox/internal/ui/components/telemetry"
)

// Apply runs only on the adapter's UI loop. Observations, not animation frames,
// own task state, progress counters, membership and log content.
func (m *Model) Apply(u Update) {
	if u.CommandID != "" {
		m.busy = false
		if u.Err != nil {
			m.Notice = fmt.Sprintf("Command %s: %v", u.CommandID, u.Err)
			// Do not offer another mutation while a submitted identity is unresolved.
			var unknown *client.UnknownSubmission
			if u.Reconcile || errors.As(u.Err, &unknown) {
				m.uncertain = u.CommandID
			}
		} else {
			m.uncertain = ""
			if u.Receipt != nil {
				m.Notice = fmt.Sprintf("Command %s: %s", u.CommandID, u.Receipt.Admission)
			}
			if u.Receipt != nil && u.Receipt.TaskID != nil {
				m.Page, m.HostID, m.TaskID, m.Tab = "Tasks", "", string(*u.Receipt.TaskID), "Overview"
			}
		}
		return
	}
	if u.Err != nil {
		if u.Resource == "" {
			m.connected = false
			m.connectionError = u.Err.Error()
		} else {
			if h := m.Host(string(u.Resource)); h != nil {
				h.CPU, h.Memory, h.Metrics = nil, "", nil
				delete(m.telemetry, h.ID)
			}
			if string(u.Resource) == m.HostID || string(u.Resource) == m.TaskID {
				m.Notice = u.Err.Error()
			}
		}
		return
	}
	if s := u.Snapshot; s != nil {
		m.connected, m.connectionError = true, ""
		m.identity = s.Identity
		if s.Credentials != nil {
			m.Credentials = s.Credentials
		}
		if s.Repositories != nil {
			m.Repositories = s.Repositories
		}
		m.warnings = strings.Join(s.Warnings, " · ")
		m.Cluster = Cluster{Gateway: string(s.Identity.NodeID)}
		if s.Identity.ClusterID != nil {
			m.Cluster.ID = string(*s.Identity.ClusterID)
			m.Cluster.Name = m.Cluster.ID
		}
		if s.Hosts != nil {
			m.Hosts = nil
		}
		for _, h := range s.Hosts {
			host := Host{Source: h, ID: string(h.ID), Name: ui.Text(h.Name, 128, ' '), Membership: string(h.Membership), Reachability: string(h.Reachability), Role: ui.Text(h.BrokerRole, 128, ' ')}
			for _, e := range h.Endpoints {
				switch e.Service {
				case "api":
					host.APIEndpoint = ui.Text("http://"+e.Address, 512, ' ')
					host.Address = ui.Text(e.Address, 512, ' ')
					if address, _, err := net.SplitHostPort(e.Address); err == nil {
						host.Address = ui.Text(address, 512, ' ')
					}
				case "nats_leaf":
					host.NATSLeaf = ui.Text("tls://"+e.Address, 512, ' ')
				}
			}
			if h.LastSeen != nil {
				host.ObservedAt = h.LastSeen.Format(time.RFC3339)
			}
			m.Hosts = append(m.Hosts, host)
		}
		if s.Tasks != nil {
			m.replaceTasks(s.Tasks)
		}
		m.Discoveries = nil
		for _, d := range s.Discoveries {
			if d.ClusterID != nil && string(*d.ClusterID) == m.Cluster.ID && d.Name != "" {
				m.Cluster.Name = ui.Text(d.Name, 128, ' ')
			}
			if d.NodeID == nil || time.Now().After(d.ExpiresAt) || m.Host(string(*d.NodeID)) != nil {
				continue
			}
			row := Discovery{ID: string(d.ID), NodeID: *d.NodeID, Name: ui.Text(d.Name, 128, ' '), Fingerprint: string(d.KeyFingerprint), ExpiresAt: d.ExpiresAt}
			for _, e := range d.Endpoints {
				if e.Service == "api" {
					row.Address = ui.Text(e.Address, 512, ' ')
					break
				}
			}
			m.Discoveries = append(m.Discoveries, row)
		}
		for _, r := range s.Requests {
			if r.State != "awaiting_approval" || time.Now().After(r.ExpiresAt) || m.Host(string(r.NodeID)) != nil {
				continue
			}
			row := ui.Find(m.Discoveries, func(d Discovery) bool { return d.NodeID == r.NodeID })
			if row == nil {
				m.Discoveries = append(m.Discoveries, Discovery{ID: string(r.RequestID), NodeID: r.NodeID, Name: string(r.NodeID)})
				row = &m.Discoveries[len(m.Discoveries)-1]
			}
			row.RequestID, row.Version, row.Requested, row.Fingerprint, row.ExpiresAt = r.RequestID, r.Version, true, string(r.KeyFingerprint), r.ExpiresAt
		}
		for id, sample := range m.telemetry {
			if m.Host(id) == nil {
				delete(m.telemetry, id)
				for key := range m.histories {
					if strings.HasPrefix(key, id+"/") {
						delete(m.histories, key)
					}
				}
			} else {
				m.projectTelemetry(sample, false)
			}
		}
	}
	if u.Tasks != nil {
		m.connected = m.identity.NodeID != ""
		m.connectionError = ""
		m.replaceTasks(u.Tasks)
	}
	if u.Task != nil {
		m.setTask(*u.Task)
	}
	if u.Telemetry != nil {
		m.projectTelemetry(*u.Telemetry, true)
	}
	if u.Log != nil && string(u.Resource) == m.TaskID {
		m.appendLog(string(u.Resource), *u.Log)
	}
}

func (m *Model) replaceTasks(tasks []domain.Task) {
	previous := m.Tasks
	m.Tasks = make([]Task, 0, len(tasks))
	for _, t := range tasks {
		row := projectTask(t)
		if old := ui.Find(previous, func(old Task) bool { return old.ID == row.ID }); old != nil {
			if old.Source.Version > t.Version {
				row = *old
			} else {
				row.Log = old.Log
			}
		}
		m.Tasks = append(m.Tasks, row)
	}
	for id := range m.bars {
		if m.Task(id) == nil {
			delete(m.bars, id)
			delete(m.logOffsets, id)
			delete(m.logPartial, id)
		}
	}
}
func projectTask(t domain.Task) Task {
	row := Task{Source: t, ID: string(t.ID), Name: t.Kind, HostID: string(t.TargetID), OwnerID: string(t.OwnerNodeID), CommandID: string(t.CommandID), Scope: string(t.Scope), State: string(t.State), Phase: t.Phase, Started: t.CreatedAt.Format(time.RFC3339), ProgressUnit: t.Progress.Unit}
	if row.HostID == "" || t.Kind == "task.cancel" || t.Kind == "task.retry" {
		row.HostID = row.OwnerID
	}
	row.Name, row.Phase, row.ProgressUnit = ui.Text(row.Name, 128, ' '), ui.Text(row.Phase, 512, ' '), ui.Text(row.ProgressUnit, 128, ' ')
	row.Age = max(0, t.UpdatedAt.Sub(t.CreatedAt))
	if t.Progress.Completed != nil {
		row.Done = int(min(*t.Progress.Completed, uint64(^uint(0)>>1)))
	}
	if t.Progress.Total != nil && t.Progress.Completed != nil {
		row.Total = int(min(*t.Progress.Total, uint64(^uint(0)>>1)))
	}
	if t.Error != nil {
		row.Result = t.Error.Error()
	} else if len(t.Result) > 0 && string(t.Result) != "null" {
		row.Result = string(t.Result)
	}
	row.Result = ui.Text(row.Result, 4096, ' ')
	return row
}
func (m *Model) setTask(t domain.Task) {
	old := m.Task(string(t.ID))
	if old != nil && old.Source.Version > t.Version {
		return
	}
	row := projectTask(t)
	if old != nil {
		row.Log = old.Log
		*old = row
	} else {
		m.Tasks = append(m.Tasks, row)
	}
}
func (m *Model) appendLog(id string, chunk domain.LogChunk) {
	task := m.Task(id)
	if task == nil {
		return
	}
	data, err := base64.StdEncoding.DecodeString(chunk.DataBase64)
	if err != nil {
		m.Notice = "Invalid log encoding"
		return
	}
	offset := m.logOffsets[id]
	if chunk.NextOffset <= offset {
		if chunk.Complete && m.logPartial[id] != "" {
			task.Log.Append(m.logPartial[id])
			m.logPartial[id] = ""
		}
		return
	}
	if chunk.Offset > offset {
		task.Log.Append("[Earlier output unavailable or omitted]")
		m.logPartial[id] = ""
	} else if offset > chunk.Offset {
		data = data[min(int(offset-chunk.Offset), len(data)):]
	}
	lines := strings.Split(m.logPartial[id]+string(data), "\n")
	for _, line := range lines[:len(lines)-1] {
		task.Log.Append(line)
	}
	m.logPartial[id] = lines[len(lines)-1]
	if len(m.logPartial[id]) > 2048 {
		task.Log.Append(m.logPartial[id])
		m.logPartial[id] = ""
	}
	if chunk.Complete && m.logPartial[id] != "" {
		task.Log.Append(m.logPartial[id])
		m.logPartial[id] = ""
	}
	m.logOffsets[id] = chunk.NextOffset
}

func (m *Model) projectTelemetry(s domain.HostTelemetry, incoming bool) {
	h := m.Host(string(s.HostID))
	if h == nil {
		return
	}
	old, exists := m.telemetry[h.ID]
	if s.MembershipEpoch != nil && h.Source.MembershipEpoch != nil && *s.MembershipEpoch != *h.Source.MembershipEpoch {
		return
	}
	if incoming && exists && old.StreamID == s.StreamID && old.Sequence >= s.Sequence {
		return
	}
	if exists && old.StreamID != s.StreamID {
		for key := range m.histories {
			if strings.HasPrefix(key, h.ID+"/") {
				delete(m.histories, key)
			}
		}
	}
	m.telemetry[h.ID] = s
	h.CPU, h.Memory, h.Metrics = nil, "", nil
	if !liveSample(s) {
		return
	}
	h.ObservedAt = s.ObservedAt.Format(time.RFC3339)
	if s.UptimeSeconds != nil {
		h.Uptime = (time.Duration(*s.UptimeSeconds) * time.Second).String()
	}
	if s.CPU != nil && s.CPU.Utilization != nil {
		h.CPU = s.CPU.Utilization
		metric := telemetry.Model{Label: "CPU", Value: fmt.Sprintf("%.1f%%", *h.CPU*100), Known: true, Fraction: *h.CPU, Tone: "accent"}
		if s.CPU.LogicalCount != nil {
			metric.Detail = fmt.Sprintf("%d logical CPUs", *s.CPU.LogicalCount)
		}
		// History contains only real observations received in this UI session.
		metric.History = m.history(h.ID+"/cpu", *h.CPU, incoming)
		h.Metrics = append(h.Metrics, metric)
	}
	if s.Memory != nil && s.Memory.UsedBytes != nil && s.Memory.TotalBytes != nil && *s.Memory.TotalBytes > 0 {
		h.Memory = fmt.Sprintf("%.1f / %.1f GiB", float64(*s.Memory.UsedBytes)/(1<<30), float64(*s.Memory.TotalBytes)/(1<<30))
		h.Metrics = append(h.Metrics, telemetry.Model{Label: "Memory", Value: h.Memory, Known: true, Fraction: float64(*s.Memory.UsedBytes) / float64(*s.Memory.TotalBytes), Tone: "running", History: m.history(h.ID+"/memory", float64(*s.Memory.UsedBytes)/float64(*s.Memory.TotalBytes), incoming)})
	}
	if s.Filesystems.Freshness == domain.Live && s.Filesystems.ObservedAt != nil && time.Since(*s.Filesystems.ObservedAt) < max(30*time.Second, time.Duration(s.Filesystems.IntervalSeconds*3*float64(time.Second))) {
		for _, fs := range s.Filesystems.Items {
			if fs.Mount == "/" && fs.UsedBytes != nil && fs.TotalBytes != nil && *fs.TotalBytes > 0 {
				h.Metrics = append(h.Metrics, telemetry.Model{Label: "Root filesystem", Value: fmt.Sprintf("%.1f / %.1f GiB", float64(*fs.UsedBytes)/(1<<30), float64(*fs.TotalBytes)/(1<<30)), Known: true, Fraction: float64(*fs.UsedBytes) / float64(*fs.TotalBytes), Tone: "success", Detail: fs.Mount})
			}
		}
	}
	for _, network := range s.Network {
		if network.RXBytesPerSecond != nil && network.TXBytesPerSecond != nil {
			history := m.history(h.ID+"/network", *network.RXBytesPerSecond, incoming)
			peak := 1.0
			for _, rate := range history {
				peak = max(peak, rate)
			}
			for i := range history {
				history[i] /= peak
			}
			h.Metrics = append(h.Metrics, telemetry.Model{Label: "Network", Value: fmt.Sprintf("↓ %.1f KiB/s · ↑ %.1f KiB/s", *network.RXBytesPerSecond/1024, *network.TXBytesPerSecond/1024), Detail: network.Interface + " · relative recent RX", Known: true, History: history, Tone: "running"})
			break
		}
	}
	for _, label := range []string{"CPU", "Memory", "Root filesystem", "Network"} {
		if !slices.ContainsFunc(h.Metrics, func(metric telemetry.Model) bool { return metric.Label == label }) {
			h.Metrics = append(h.Metrics, telemetry.Model{Label: label, Value: "—", Detail: "No live sample", Tone: "muted"})
		}
	}
}

func liveSample(s domain.HostTelemetry) bool {
	return s.Freshness == domain.Live && s.ObservedAt != nil && time.Since(*s.ObservedAt) <= max(5*time.Second, time.Duration(s.IntervalSeconds*3*float64(time.Second)))
}
func (m *Model) history(key string, value float64, appendSample bool) []float64 {
	history := m.histories[key]
	if appendSample {
		history = append(history, value)
		if len(history) > 60 {
			history = history[len(history)-60:]
		}
		m.histories[key] = history
	}
	return slices.Clone(history)
}
