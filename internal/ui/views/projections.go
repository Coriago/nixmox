package views

import (
	"maps"
	"slices"
	"strings"

	"nixmox/internal/ui"
	"nixmox/internal/ui/components/status"
	"nixmox/internal/ui/components/table"
)

func clean(value string) string                      { return ui.Text(value, 500, ' ') }
func sortedKeys[T any](values map[string]T) []string { return slices.Sorted(maps.Keys(values)) }
func safeLines(value string) []string {
	lines := strings.Split(value, "\n")
	if len(lines) > 4000 {
		lines = append(lines[:4000], "Preview truncated")
	}
	for i := range lines {
		lines[i] = ui.Text(lines[i], 2000, ' ')
	}
	return lines
}
func (m *Model) hostLink(host Host) HostLink {
	return HostLink{ID: host.ID, Title: host.Name, State: host.Membership, Marker: "○", Tone: status.MembershipTone(host.Membership), Action: Action{Kind: "host", ID: host.ID}}
}
func (m *Model) hostTable(title, query string) table.Model {
	result := table.Model{Title: title, Columns: []string{"Host", "Membership", "Endpoint"}, Empty: "No hosts"}
	for _, host := range m.Hosts {
		if !matches(query, host.Name, host.ID, host.Address) {
			continue
		}
		result.Records = append(result.Records, table.Record{ID: host.ID, Action: Action{Kind: "host", ID: host.ID}, Marker: "○", Cells: []table.Cell{{Text: host.Name, Detail: host.ID}, {Text: host.Membership}, {Text: host.Address}}})
	}
	return result
}
func (m *Model) taskTable(title, hostID, filter, query string) table.Model {
	result := table.Model{Title: title, Columns: []string{"Task", "Status", "Host"}, Empty: "No matching tasks"}
	for _, task := range m.Tasks {
		if hostID != "" && task.HostID != hostID {
			continue
		}
		if filter == "Active" && !ui.Active(task.State) {
			continue
		}
		if filter == "Needs attention" && task.State != "failed" && task.State != "unknown" && task.State != "manual-review" {
			continue
		}
		if !matches(query, task.ID, task.Name, task.HostID, task.State) {
			continue
		}
		marker, tone := status.Marker(task.State, m.frame, m.Reduced)
		result.Records = append(result.Records, table.Record{ID: task.ID, Action: Action{Kind: "task", ID: task.ID}, Marker: marker, Tone: tone, Cells: []table.Cell{{Text: task.Name, Detail: task.ID}, {Text: task.State, Tone: tone}, {Text: task.HostID}}})
	}
	return result
}
