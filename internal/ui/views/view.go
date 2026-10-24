package views

import (
	"strings"

	"nixmox/internal/ui"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/confirmation"
	"nixmox/internal/ui/components/section"
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
	Reduced                                               bool
	Workspace, Connection                                 string
	Nav, HeaderActions, Toolbar                           []button.Model
	Hosts                                                 []HostLink
	Blocks                                                []Block
	Modal                                                 *confirmation.Model
	HostID                                                string
}

func control(label, kind, id string, active bool) button.Model {
	return button.Model{Label: label, Action: Action{Kind: kind, ID: id}, Active: active}
}

func field(label, value string) section.Field { return section.Field{Label: label, Value: value} }

func (m *Model) View() View {
	workspace := m.Cluster.Name
	if workspace == "" {
		workspace = "No cluster"
	}
	v := View{Page: m.Page, Title: m.Page, Subtitle: workspace, Workspace: workspace, Connection: ui.Text(m.connectionLabel(), 2048, ' '), Breadcrumb: strings.ToUpper(workspace + " / " + m.Page), Tab: m.Tab, Theme: m.Theme(), Reduced: m.Reduced, Notice: ui.Text(m.Notice, 2048, ' '), Modal: m.Modal, HostID: m.HostID, Query: m.Query}
	v.Toolbar = []button.Model{
		control("Theme", "theme", "", false),
		control("Reduced motion", "motion", "", m.Reduced),
		control("Help", "help", "", false),
	}
	if m.uncertain != "" {
		v.Toolbar = append(v.Toolbar, control("Check command receipt", "reconcile", "", false))
	}
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
func (m *Model) connectionLabel() string {
	if m.connected {
		if m.warnings != "" {
			return "Connected · " + m.warnings
		}
		return "Connected"
	}
	if m.connectionError != "" {
		return "Disconnected · " + m.connectionError
	}
	return "Connecting…"
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
	return unknown(s)
}
func taskResult(t Task) string {
	if t.Result != "" {
		return t.Result
	}
	return "No final result yet. Execution is " + t.State + ". Closing this view does not cancel work."
}
