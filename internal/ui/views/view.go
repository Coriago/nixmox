package views

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"nixmox/internal/ui"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/confirmation"
	"nixmox/internal/ui/components/progress"
	"nixmox/internal/ui/components/section"
	"nixmox/internal/ui/components/status"
	"nixmox/internal/ui/components/table"
	"nixmox/internal/ui/components/telemetry"
	"nixmox/internal/ui/components/theme"
)

type HostLink struct {
	ID, Title, State, Marker, Tone string
	Action                         ui.Action
	Tasks                          int
}

// Blocks are the page definition: ordered data consumed directly by both adapters.
type View struct {
	Page, Title, Subtitle, Breadcrumb, Tab, Notice, Query string
	Theme                                                 theme.Theme
	Reduced, Paused                                       bool
	Nav, HeaderActions                                    []button.Model
	Hosts                                                 []HostLink
	Blocks                                                []Block
	Modal                                                 *confirmation.Model
	HostID                                                string
}

func control(label, kind, id string, active bool) button.Model {
	return button.Model{Label: label, Action: Action{Kind: kind, ID: id}, Active: active}
}
func field(label, value string) section.Field { return section.Field{Label: label, Value: value} }

func (m *Model) hostLink(h Host) HostLink {
	link := HostLink{ID: h.ID, Title: h.Name, State: h.Reachability, Action: Action{Kind: "host", ID: h.ID}}
	for _, t := range m.Tasks {
		if t.HostID == h.ID && ui.Active(t.State) {
			link.Tasks++
		}
	}
	state := h.Reachability
	if link.Tasks > 0 && state == "online" {
		state = "running"
	}
	link.Marker, link.Tone = status.Marker(state, m.frame, m.Reduced)
	return link
}
func (m *Model) taskRecord(t Task) table.Record {
	marker, tone := status.Marker(t.State, m.frame, m.Reduced)
	label, value := "", 0.0
	if ui.Active(t.State) {
		label = "Indeterminate"
	}
	if t.Total > 0 {
		value = float64(t.Done) / float64(t.Total)
		label = fmt.Sprintf("%d / %d endpoints · %.0f%%", t.Done, t.Total, value*100)
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
func (m *Model) taskTable(title, host, filter string) table.Model {
	tasks := []Task{}
	for _, t := range m.Tasks {
		if host != "" && t.HostID != host || filter == "Active" && !ui.Active(t.State) || filter == "Needs attention" && t.State != "blocked" && t.State != "failed" {
			continue
		}
		if m.Page == "Tasks" && m.TaskID == "" && !matches(m.Query, t.ID, t.Name, t.HostID, t.State, t.Phase) {
			continue
		}
		tasks = append(tasks, t)
	}
	slices.SortStableFunc(tasks, func(a, b Task) int { return cmp.Compare(b.Started, a.Started) })
	list := table.Model{Title: title, Empty: "No tasks match this view.", Columns: []string{"Task / target", "Status", "Phase / progress", "Started", "Duration"}}
	for _, t := range tasks {
		list.Records = append(list.Records, m.taskRecord(t))
	}
	return list
}
func (m *Model) hostTable(title string) table.Model {
	list := table.Model{Title: title, Columns: []string{"Host / address", "Reachability", "Membership", "CPU", "Memory", "Active tasks"}}
	for _, h := range m.Hosts {
		if m.Page == "Hosts" && m.HostID == "" && !matches(m.Query, h.ID, h.Name, h.Address, h.Reachability, h.Membership) {
			continue
		}
		r := m.hostLink(h)
		cpu, memory := "—", "—"
		if h.Reachability == "online" {
			cpu = fmt.Sprintf("%.0f%%", h.CPU*100)
			memory = h.Memory
		}
		list.Records = append(list.Records, table.Record{ID: h.ID, Marker: r.Marker, Tone: r.Tone, Action: r.Action, Cells: []table.Cell{
			{Text: h.Name, Detail: h.Address}, {Text: h.Reachability, Tone: r.Tone}, {Text: h.Membership, Tone: status.MembershipTone(h.Membership), Badge: true}, {Text: cpu}, {Text: memory}, {Text: fmt.Sprint(r.Tasks)},
		}})
	}
	return list
}
func hostMetrics(h Host) []telemetry.Model {
	if h.Reachability != "online" {
		return []telemetry.Model{{Label: "CPU", Value: "—", Detail: "No live sample", Tone: "muted"}, {Label: "Memory", Value: "—", Detail: "No live sample", Tone: "muted"}, {Label: "Root filesystem", Value: "—", Detail: "No live sample", Tone: "muted"}, {Label: "Network", Value: "—", Detail: "No live sample", Tone: "muted"}}
	}
	memory, network := make([]float64, len(h.History)), make([]float64, len(h.History))
	for i, cpu := range h.History {
		memory[i] = h.MemoryUsedGiB/h.MemoryGiB + (cpu-h.CPU)*.015
		network[i] = .1 + cpu*.7
	}
	return []telemetry.Model{
		{Label: "CPU", Value: fmt.Sprintf("%.1f%%", h.CPU*100), Detail: fmt.Sprintf("%d logical CPUs · load 0.8 / 0.6 / 0.4", h.Cores), Tone: "accent", Known: true, Fraction: h.CPU, History: slices.Clone(h.History)},
		{Label: "Memory", Value: h.Memory, Detail: "Used / capacity · swap 0 / 4 GiB", Tone: "running", Known: true, Fraction: h.MemoryUsedGiB / h.MemoryGiB, History: memory},
		{Label: "Root filesystem", Value: "128 / 512 GB", Detail: "/ · 25% used · 380 GB available", Tone: "success", Known: true, Fraction: .25},
		{Label: "Network", Value: fmt.Sprintf("↓ %.2f MiB/s", .5+h.CPU*4), Detail: "enp1s0 · TX 140 KiB/s · mock rates", Tone: "running", Known: true, Fraction: .1 + h.CPU*.7, History: network},
	}
}

func (m *Model) View() View {
	v := View{Page: m.Page, Title: m.Page, Subtitle: "homelab", Breadcrumb: "HOMELAB / " + strings.ToUpper(m.Page), Tab: m.Tab, Theme: m.Theme(), Reduced: m.Reduced, Paused: m.Paused, Notice: m.Notice, Modal: m.Modal, HostID: m.HostID, Query: m.Query}
	for _, p := range Pages {
		v.Nav = append(v.Nav, control(p.Name, "page", p.Name, m.Page == p.Name))
		if p.Name == m.Page {
			p.Build(m, &v)
		}
	}
	for _, h := range m.Hosts {
		v.Hosts = append(v.Hosts, m.hostLink(h))
	}
	v.Blocks = append([]Block{{Kind: Heading}}, v.Blocks...)
	if v.Notice != "" {
		v.Add(Block{Kind: Notice})
	}
	return v
}
func matches(query string, values ...string) bool {
	return query == "" || strings.Contains(strings.ToLower(strings.Join(values, " ")), strings.ToLower(query))
}
func unknown(s string) string {
	if s == "" {
		return "Unknown"
	}
	return s
}
func liveValue(h Host, s string) string {
	if h.Reachability != "online" {
		return "Unavailable · no live observation"
	}
	return s
}
func taskResult(t Task) string {
	if t.Result != "" {
		return t.Result
	}
	return "No final result yet. Execution is " + t.State + ". Closing this view does not cancel work."
}
