package views

import (
	"encoding/json"
	"fmt"

	"nixmox/internal/domain"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/section"
)

type Page struct {
	Name  string
	Build func(*Model, *View)
}

var Pages = []Page{
	{"Cluster", clusterPage}, {"Hosts", hostsPage}, {"Tasks", tasksPage},
	{"Repositories", repositoriesPage}, {"Recipes", recipesPage},
	{"Configurations", configurationsPage}, {"Settings", settingsPage},
}

func clusterPage(m *Model, v *View) {
	v.Subtitle = v.Connection
	if m.Cluster.ID == "" && m.Health.Status == "online" {
		v.HeaderActions = []button.Model{m.action("Create cluster", "cluster-create", "")}
	}
	v.Add(Block{Kind: Identity, Fields: []section.Field{
		field("Cluster", unknown(m.Cluster.Name)), field("Cluster ID", unknown(m.Cluster.ID)), field("Local node", unknown(m.Health.NodeID)), field("Daemon health", unknown(m.Health.Status)),
	}})
	for _, name := range sortedKeys(m.Health.Checks) {
		v.Add(Block{Kind: Sections, Section: section.Model{Title: name, Note: m.Health.Checks[name].Status}})
	}
	v.Add(Block{Kind: Controls, Buttons: []button.Model{unavailable("Enrollment"), unavailable("Membership changes")}}, Block{Kind: Tables, Table: m.hostTable("Cluster hosts", "")})
	m.formBlocks(v)
}
func hostsPage(m *Model, v *View) {
	if host := m.Host(m.HostID); host != nil {
		v.Title = host.Name
		build := m.action("Build Nix installable", "build", host.ID)
		if host.ID != m.Health.NodeID {
			build.Disabled = true
			build.Hint = "Remote build dispatch is not implemented"
		}
		v.HeaderActions = []button.Model{control("Back", "back", "", false), build}
		v.Add(Block{Kind: Identity, Fields: []section.Field{field("Node ID", host.ID), field("Membership", host.Membership), field("Endpoint", unknown(host.Address)), field("Environment", host.Source.Environment), field("Version", host.Source.Daemon.Version)}})
		v.Add(Block{Kind: Sections, Section: section.Model{Title: "Live host measurements", Note: "CPU, memory, disks and network telemetry are not exposed by this backend. Membership is not a health measurement."}})
		v.Add(Block{Kind: Tables, Table: m.taskTable("Host tasks", host.ID, "All", "")}, Block{Kind: Logs, Log: &m.HostLog})
		m.formBlocks(v)
		return
	}
	v.Add(Block{Kind: Search}, Block{Kind: Tables, Table: m.hostTable("Hosts", m.Query)})
}
func tasksPage(m *Model, v *View) {
	if task := m.Task(m.TaskID); task != nil {
		v.Title = task.Name
		v.Subtitle = task.ID
		v.HeaderActions = []button.Model{control("Back", "back", "", false)}
		if task.Source.HostID == m.Health.NodeID {
			if task.Source.Status.Terminal() {
				v.HeaderActions = append(v.HeaderActions, m.action("Retry", "retry", task.ID))
			} else {
				v.HeaderActions = append(v.HeaderActions, m.action("Cancel…", "cancel", task.ID))
			}
			if task.Source.Status == domain.ManualReview && task.Source.Review != nil && task.Source.Review.ApprovedAt == nil {
				v.HeaderActions = append(v.HeaderActions, m.action("Read review output", "task-output", task.Source.Review.OutputID), m.action("Approve review", "approve", task.ID))
			}
		}
		v.Add(Block{Kind: Identity, Fields: []section.Field{field("Status", task.State), field("Host", task.HostID), field("Created", task.Source.CreatedAt.Format("2006-01-02 15:04:05"))}})
		if task.Source.Type == "build" {
			v.Add(Block{Kind: Build, Build: &task.Build})
		}
		tabs := Block{Kind: Tabs}
		for _, name := range []string{"Overview", "Logs", "Result"} {
			tabs.Buttons = append(tabs.Buttons, control(name, "tab", name, m.Tab == name))
		}
		v.Add(tabs)
		switch m.Tab {
		case "Logs":
			v.Add(Block{Kind: Logs, Log: &task.Log})
		case "Result":
			data, _ := json.Marshal(task.Source)
			v.Add(Block{Kind: Preview, Lines: pretty(data)})
		default:
			v.Add(Block{Kind: Sections, Section: section.Model{Title: "Task execution", Note: "Closing this view does not cancel the task. Stops in Nix activity output are not success claims; task status is authoritative."}})
			for _, output := range task.Source.Links.Outputs {
				v.Add(Block{Kind: Controls, Buttons: []button.Model{m.action("Read "+output.Kind, "task-output", output.ID)}})
			}
		}
		if len(m.Preview) > 0 {
			v.Add(Block{Kind: Preview, Lines: pretty(m.Preview)})
		}
		return
	}
	if m.TaskID != "" {
		v.Subtitle = "Waiting for task state"
		return
	}
	v.Subtitle = fmt.Sprintf("%d recorded tasks", len(m.Tasks))
	filters := Block{Kind: Controls}
	for _, filter := range []string{"All", "Active", "Needs attention"} {
		filters.Buttons = append(filters.Buttons, control(filter, "filter", filter, m.Filter == filter))
	}
	v.Add(Block{Kind: Search}, filters, Block{Kind: Tables, Table: m.taskTable("Tasks", "", m.Filter, m.Query)})
}
func settingsPage(m *Model, v *View) {
	v.Add(Block{Kind: Sections, Section: section.Model{Title: "Appearance", Note: "Appearance is local to this session.", Controls: []button.Model{control("Theme", "theme", "", false), control("Accent", "accent", "", false), control("Reduced motion", "motion", "", m.Reduced)}}})
	v.Add(Block{Kind: Sections, Section: section.Model{Title: "Unavailable services", Note: "Credential management, enrollment, deployment, installation, publication, power controls and live hardware telemetry have no backend routes yet. They are not simulated."}})
	v.Add(Block{Kind: Sections, Section: section.Model{Title: "Nix cache", Note: "The daemon exposes /cache/nix-cache-info and /store-cache/nix-cache-info for Nix clients. Cache administration is not implemented."}})
}
