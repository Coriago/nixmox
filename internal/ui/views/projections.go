package views

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"nixmox/internal/ui"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/progress"
	"nixmox/internal/ui/components/status"
	"nixmox/internal/ui/components/table"
	"nixmox/internal/ui/components/telemetry"
)

func (m *Model) hostLink(h Host) HostLink {
	link := HostLink{ID: h.ID, Title: h.Name, State: h.Reachability, Action: Action{Kind: "host", ID: h.ID}}
	for _, t := range m.Tasks {
		if t.HostID == h.ID && ui.Active(t.State) {
			link.Tasks++
		}
	}
	state := h.Reachability
	if !m.connected && state == "online" {
		state = "stale"
		link.State = "stale"
	}
	if link.Tasks > 0 && state == "online" {
		state = "running"
	}
	if link.Tasks > 0 {
		link.State += fmt.Sprintf(" · %d active", link.Tasks)
	}
	link.Marker, link.Tone = status.Marker(state, m.frame, m.Reduced)
	return link
}
func (m *Model) taskRecord(t Task) table.Record {
	marker, tone := status.Marker(t.State, m.frame, m.Reduced)
	label, value := "", 0.0
	if ui.Active(t.State) {
		if !m.connected {
			marker, tone = status.Marker("stale", m.frame, true)
		}
		label = "Indeterminate"
	}
	if t.Total > 0 {
		value = float64(t.Done) / float64(t.Total)
		label = fmt.Sprintf("%d / %d %s · %.0f%%", t.Done, t.Total, t.ProgressUnit, value*100)
	}
	s := m.bars[t.ID]
	if s == nil {
		s = &progress.Spring{}
	}
	bar := s.Bar(value, t.Total > 0, label)
	return table.Record{ID: t.ID, Marker: marker, Tone: tone, Action: Action{Kind: "task", ID: t.ID}, Cells: []table.Cell{
		{Text: t.Name, Detail: t.HostID}, {Text: t.State, Tone: tone}, {Text: t.Phase, Detail: label, Bar: bar}, {Text: t.Started}, {Text: fmt.Sprintf("%ds", int(t.Age.Seconds()))},
	}}
}
func taskList(title string, records ...table.Record) table.Model {
	return table.Model{Title: title, Empty: "No tasks match this view.", Columns: []string{"Task / target", "Status", "Phase / progress", "Started", "Duration"}, Records: records}
}

func (m *Model) taskTable(title, host, filter, query string) table.Model {
	tasks := []Task{}
	for _, t := range m.Tasks {
		if host != "" && t.HostID != host || filter == "Active" && !ui.Active(t.State) || filter == "Needs attention" && t.State != "blocked" && t.State != "failed" {
			continue
		}
		if !matches(query, t.ID, t.Name, t.HostID, t.State, t.Phase) {
			continue
		}
		tasks = append(tasks, t)
	}
	slices.SortStableFunc(tasks, func(a, b Task) int { return cmp.Compare(b.Started, a.Started) })
	list := taskList(title)
	for _, t := range tasks {
		list.Records = append(list.Records, m.taskRecord(t))
	}
	return list
}
func (m *Model) hostTable(title, query string) table.Model {
	list := table.Model{Title: title, Empty: "No host observations available.", Columns: []string{"Host / address", "Reachability", "Membership", "CPU", "Memory", "Active tasks"}}
	for _, h := range m.Hosts {
		if !matches(query, h.ID, h.Name, h.Address, h.Reachability, h.Membership) {
			continue
		}
		r := m.hostLink(h)
		cpu, memory := "—", "—"
		if h.Reachability == "online" {
			if h.CPU != nil {
				cpu = fmt.Sprintf("%.0f%%", *h.CPU*100)
			}
			if h.Memory != "" {
				memory = h.Memory
			}
		}
		list.Records = append(list.Records, table.Record{ID: h.ID, Marker: r.Marker, Tone: r.Tone, Action: r.Action, Cells: []table.Cell{
			{Text: h.Name, Detail: h.Address}, {Text: h.Reachability, Tone: reachabilityTone(h.Reachability)}, {Text: h.Membership, Tone: status.MembershipTone(h.Membership), Badge: true}, {Text: cpu}, {Text: memory}, {Text: fmt.Sprint(r.Tasks)},
		}})
	}
	return list
}
func hostMetrics(h Host) []telemetry.Model {
	if h.Reachability == "online" && len(h.Metrics) > 0 {
		return slices.Clone(h.Metrics)
	}
	var metrics []telemetry.Model
	for _, label := range []string{"CPU", "Memory", "Root filesystem", "Network"} {
		metrics = append(metrics, telemetry.Model{Label: label, Value: "—", Detail: "No live sample", Tone: "muted"})
	}
	return metrics
}

func reachabilityTone(state string) string {
	_, tone := status.Marker(state, 0, true)
	return tone
}

func (m *Model) discoveryTable() table.Model {
	t := table.Model{Title: "Discovery", Empty: "No discovered hosts awaiting enrollment.", Columns: []string{"Host / address", "Membership", "Status", "Public fingerprint", "Actions"}}
	discovered := slices.Clone(m.Discoveries)
	slices.SortStableFunc(discovered, func(a, b Discovery) int {
		if a.Requested != b.Requested {
			if a.Requested {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	for _, d := range discovered {
		if m.Host(string(d.NodeID)) != nil || time.Now().After(d.ExpiresAt) {
			continue
		}
		label, tone := "Discovered", "muted"
		var controls []button.Model
		if d.Requested {
			label, tone = "Awaiting approval", "warning"
			controls = []button.Model{m.resourceControl("Approve", "approve", d.ID), m.resourceControl("Reject", "reject", d.ID)}
		} else if d.Decision != "" {
			label = d.Decision
		}
		t.Records = append(t.Records, table.Record{ID: d.ID, Marker: "○", Tone: "muted", Controls: controls, Cells: []table.Cell{
			{Text: d.Name, Detail: d.Address}, {Text: "unjoined", Tone: status.MembershipTone("unjoined"), Badge: true}, {Text: label, Tone: tone, Badge: d.Requested}, {Text: d.Fingerprint}, {},
		}})
	}
	return t
}
