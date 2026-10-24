package views

import (
	"fmt"
	"slices"
	"strings"

	"nixmox/internal/ui"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/section"
	"nixmox/internal/ui/components/status"
)

type Page struct {
	Name  string
	Build func(*Model, *View)
}

var Pages = []Page{{"Cluster", clusterPage}, {"Hosts", hostsPage}, {"Tasks", tasksPage}, {"Settings", settingsPage}}

func hostsPage(m *Model, v *View) {
	if h := m.Host(m.HostID); h != nil {
		v.Title = h.Name
		v.Subtitle = h.Address + " · " + h.Reachability + " · " + h.Role
		v.Breadcrumb = strings.ToUpper(v.Workspace + " / Hosts / " + h.Name)
		v.HeaderActions = []button.Model{m.resourceControl("Refresh", "refresh", h.ID), m.resourceControl("Network check", "network-check", h.ID), m.resourceControl("Shutdown…", "shutdown", h.ID)}
		_, tone := status.Marker(h.Reachability, m.frame, m.Reduced)
		v.Add(
			Block{Kind: Identity, Fields: []section.Field{{Label: "Reachability", Value: h.Reachability, Tone: tone}, {Label: "Membership", Value: h.Membership, Tone: status.MembershipTone(h.Membership)}, field("Node ID", h.ID), field("Operating system", unknown(h.OS)), field("Processor", unknown(h.Processor)), field("Architecture", unknown(h.Architecture)), field("Uptime", unknown(h.Uptime)), field("Health", "Unknown · checks unavailable")}},
			Block{Kind: Metrics, Metrics: hostMetrics(*h)},
			Block{Kind: Tables, Table: m.taskTable("Task history", h.ID, "All", "")},
			Block{Kind: Sections, Section: section.Model{Title: "Connectivity & storage", Fields: []section.Field{field("API endpoint", unknown(h.APIEndpoint)), field("NATS leaf", unknown(h.NATSLeaf)), field("Disk I/O", liveValue(*h, h.DiskIO)), field("Last observation", unknown(h.ObservedAt))}, Note: "Unavailable measurements are not zero."}},
		)
		return
	}
	online := 0
	for _, h := range m.Hosts {
		if h.Reachability == "online" {
			online++
		}
	}
	v.Subtitle = fmt.Sprintf("%d enrolled hosts · %d online · health checks separate from connectivity", len(m.Hosts), online)
	v.HeaderActions = []button.Model{control("Discovery", "page", "Cluster", false)}
	v.Add(Block{Kind: Search}, Block{Kind: Tables, Table: m.hostTable("Host inventory", m.Query)})
}
func tasksPage(m *Model, v *View) {
	if t := m.Task(m.TaskID); t != nil {
		v.Title = t.Name
		v.Subtitle = t.HostID + " · " + t.ID
		v.Breadcrumb = strings.ToUpper(v.Workspace + " / Tasks / " + t.ID)
		v.HeaderActions = []button.Model{control("Back to tasks", "back", "", false)}
		v.HeaderActions = append(v.HeaderActions, m.resourceControl("Cancel task…", "cancel", t.ID))
		if d := m.discovery(t.HostID); t.State == "blocked" && d != nil && d.Requested {
			v.HeaderActions = append(v.HeaderActions, control("Show discovery", "page", "Cluster", false))
		}
		r := m.taskRecord(*t)
		v.Add(Block{Kind: Identity, Fields: []section.Field{{Label: "Status", Value: t.State, Tone: r.Tone}, field("Target", t.HostID), field("Started", t.Started), field("Duration", fmt.Sprintf("%ds", int(t.Age.Seconds()))), field("Owner", unknown(t.OwnerID)), field("Command ID", unknown(t.CommandID))}})
		tabs := Block{Kind: Tabs}
		for _, name := range []string{"Overview", "Logs", "Result"} {
			tabs.Buttons = append(tabs.Buttons, control(name, "tab", name, m.Tab == name))
		}
		v.Add(tabs)
		switch v.Tab {
		case "Overview":
			v.Add(
				Block{Kind: Sections, Section: section.Model{Title: "Execution state", Fields: []section.Field{field("Current phase", t.Phase), field("Scope", unknown(t.Scope))}, Note: taskResult(*t)}},
				Block{Kind: Tables, Table: taskList("Execution", r)},
				Block{Kind: Preview, Lines: slices.Clone(t.Log.Lines[max(0, len(t.Log.Lines)-6):])},
			)
		case "Logs":
			v.Add(Block{Kind: Logs, Log: &t.Log})
		case "Result":
			v.Add(Block{Kind: Sections, Section: section.Model{Title: "Observed outcome", Fields: []section.Field{field("Task", t.ID), field("State", t.State)}, Note: taskResult(*t)}})
		}
		return
	}
	if m.TaskID != "" {
		v.Title = "Task " + m.TaskID
		v.Subtitle = "Waiting for the executor snapshot"
		v.HeaderActions = []button.Model{control("Back to tasks", "back", "", false)}
		return
	}
	active, attention := 0, 0
	for _, t := range m.Tasks {
		if ui.Active(t.State) {
			active++
		}
		if t.State == "blocked" || t.State == "failed" {
			attention++
		}
	}
	v.Subtitle = fmt.Sprintf("%d tasks · %d active · %d need attention", len(m.Tasks), active, attention)
	filters := Block{Kind: Controls}
	for _, f := range []string{"All", "Active", "Needs attention"} {
		filters.Buttons = append(filters.Buttons, control(f, "filter", f, m.Filter == f))
	}
	v.Add(Block{Kind: Search}, filters, Block{Kind: Tables, Table: m.taskTable("Tasks", "", m.Filter, m.Query)})
}
func clusterPage(m *Model, v *View) {
	v.Subtitle = v.Connection
	full, pending := 0, 0
	for _, h := range m.Hosts {
		if h.Membership == "full" {
			full++
		}
		if h.Membership == "pending" {
			pending++
		}
	}
	v.Add(Block{Kind: Identity, Fields: []section.Field{field("Cluster ID", unknown(m.Cluster.ID)), field("Full members", fmt.Sprint(full)), field("Pending members", fmt.Sprint(pending)), field("Gateway", unknown(m.Cluster.Gateway))}})
	if discovery := m.discoveryTable(); len(discovery.Records) > 0 {
		v.Add(Block{Kind: Tables, Table: discovery})
	}
	v.Add(Block{Kind: Tables, Table: m.hostTable("Cluster hosts", "")}, Block{Kind: Tables, Table: m.taskTable("Active tasks", "", "Active", "")})
}
func settingsPage(m *Model, v *View) {
	v.Subtitle = "Appearance, connection and effective configuration"
	motion := "Full"
	if m.Reduced {
		motion = "Reduced"
	}
	v.Add(
		Block{Kind: Sections, Section: section.Model{Title: "Theme", Fields: []section.Field{field("Current palette", v.Theme.Name), field("Default accent", "Proxmox orange · #e57000")}, Controls: []button.Model{control("Theme · "+v.Theme.Name, "theme", "", false), control("Accent color", "accent", "", false)}, Note: "Appearance changes apply to this session."}},
		Block{Kind: Sections, Section: section.Model{Title: "Animation & accessibility", Fields: []section.Field{field("Motion", motion), field("Input", "Keyboard and mouse"), field("Logs", "Follow until scrolled upward")}, Controls: []button.Model{control("Reduced motion", "motion", "", m.Reduced)}, Note: "Reduced motion stops spinners and snaps animated progress to observations."}},
		Block{Kind: Sections, Section: section.Model{Title: "Current connection", Fields: []section.Field{field("Status", v.Connection), field("Gateway", unknown(m.Cluster.Gateway))}}},
		Block{Kind: Sections, Section: section.Model{Title: "Effective configuration", Fields: []section.Field{field("Source", "Unavailable")}, Note: "Effective runtime configuration is not exposed by this API yet."}},
	)
}
