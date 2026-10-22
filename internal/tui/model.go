// Package tui contains terminal rendering only; all actions use the console API.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"nixmox/internal/client"
	"nixmox/internal/domain"
	"nixmox/internal/presentation"
)

type snapshot struct {
	network    domain.NetworkStatus
	tasks      []domain.Task
	err        error
	cluster    domain.ClusterStatus
	clusterErr error
}
type submitted struct {
	receipt domain.Receipt
	err     error
}
type event struct{ err error }
type Model struct {
	client       *client.Client
	ctx          context.Context
	events       <-chan error
	viewport     viewport.Model
	network      domain.NetworkStatus
	tasks        []domain.Task
	notice       string
	pending      *domain.Command
	submitting   bool
	width        int
	taskFocus    bool
	selected     int
	fetching     bool
	refreshAgain bool
	cluster      domain.ClusterStatus
	clusterError string
	clusterFocus bool
	creating     bool
	nameInput    textinput.Model
}

func New(ctx context.Context, c *client.Client) Model {
	name := textinput.New()
	name.Placeholder = "Cluster name"
	name.CharLimit = 64
	name.Width = 40
	return Model{client: c, ctx: ctx, events: c.Events(ctx), viewport: viewport.New(76, 18), width: 80, notice: "Connecting…", fetching: true, nameInput: name}
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.fetch(), m.waitEvent()) }
func (m Model) refresh() (Model, tea.Cmd) {
	if m.fetching {
		m.refreshAgain = true
		return m, nil
	}
	m.fetching = true
	return m, m.fetch()
}
func (m Model) fetch() tea.Cmd {
	return func() tea.Msg {
		n, err := m.client.Network(m.ctx)
		if err != nil {
			return snapshot{err: err}
		}
		p, err := m.client.Tasks(m.ctx, "?limit=100")
		cluster, clusterErr := m.client.Cluster(m.ctx)
		return snapshot{network: n, tasks: p.Items, err: err, cluster: cluster, clusterErr: clusterErr}
	}
}
func (m Model) waitEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return tea.Quit()
		case err, ok := <-m.events:
			if !ok {
				return tea.Quit()
			}
			return event{err}
		}
	}
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && m.creating {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.creating = false
			m.nameInput.Blur()
			return m, nil
		case "enter":
			if m.cluster.Membership != "unjoined" || m.clusterError != "" {
				m.notice = "Cluster creation requires a current unjoined node."
				return m, nil
			}
			payload, _ := json.Marshal(map[string]string{"name": m.nameInput.Value()})
			m.creating = false
			m.nameInput.Blur()
			return m.submit(domain.Command{ID: domain.NewID("cmd_"), Type: "cluster.create", TargetID: m.network.NodeID, Payload: payload, ExpectedVersions: map[string]int64{}})
		default:
			var cmd tea.Cmd
			m.nameInput, cmd = m.nameInput.Update(msg)
			return m, cmd
		}
	}
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(20, v.Width)
		m.viewport.Width = max(10, v.Width-4)
		m.viewport.Height = max(1, v.Height-9)
		m.content()
		return m, nil
	case snapshot:
		m.fetching = false
		var next tea.Cmd
		if m.refreshAgain {
			m.refreshAgain = false
			m, next = m.refresh()
		}
		if v.err != nil {
			m.notice = v.err.Error()
			return m, next
		}
		selectedID := ""
		if m.selected < len(m.tasks) {
			selectedID = m.tasks[m.selected].ID
		}
		m.network = v.network
		m.cluster = v.cluster
		m.clusterError = ""
		if v.clusterErr != nil {
			m.clusterError = v.clusterErr.Error()
		}
		m.tasks = v.tasks
		m.selected = 0
		for index, task := range m.tasks {
			if task.ID == selectedID {
				m.selected = index
			}
		}
		m.content()
		return m, next
	case event:
		if v.err != nil {
			m.notice = v.err.Error()
			return m, m.waitEvent()
		}
		m.notice = "Live updates connected"
		if m.pending != nil && !m.submitting {
			m.notice = "Uncertain command " + m.pending.ID + " · press p to retry the same request"
		}
		m, cmd := m.refresh()
		return m, tea.Batch(cmd, m.waitEvent())
	case submitted:
		m.submitting = false
		if v.err != nil {
			m.notice = fmt.Sprintf("%s · retry ID %s", v.err, m.pending.ID)
			var e *domain.Error
			if errors.As(v.err, &e) && e.Status >= 400 && e.Status < 500 {
				m.pending = nil
			}
			return m, nil
		}
		m.notice = "Accepted " + v.receipt.CommandID + " · " + v.receipt.TaskID
		m.pending = nil
		m, cmd := m.refresh()
		return m, cmd
	case tea.KeyMsg:
		switch v.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "?":
			m.notice = "Tab: Network/Tasks · g: Cluster · Enter/r: check · ↑/↓: select task · c: cancel/create · r: retry · p: retry uncertain submission · PgUp/PgDn: scroll · q: close"
			return m, nil
		case "g":
			m.clusterFocus = !m.clusterFocus
			m.taskFocus = false
			m.content()
			m.viewport.GotoTop()
			return m, nil
		case "tab", "shift+tab":
			m.clusterFocus = false
			m.taskFocus = !m.taskFocus
			m.content()
			m.viewport.GotoTop()
			return m, nil
		case "up", "down":
			if m.taskFocus && len(m.tasks) > 0 {
				if v.String() == "up" {
					m.selected = max(0, m.selected-1)
				} else {
					m.selected = min(len(m.tasks)-1, m.selected+1)
				}
				m.content()
				m.viewport.GotoTop()
				return m, nil
			}
		case "p":
			if m.pending != nil && !m.submitting {
				return m.submit(*m.pending)
			}
			return m, nil
		case "enter", "r", "c":
			if m.submitting || m.network.NodeID == "" {
				return m, nil
			}
			if m.pending != nil {
				m.notice = "An uncertain submission is pending. Press p to retry its exact command."
				return m, nil
			}
			if m.clusterFocus {
				if v.String() != "c" {
					return m, nil
				}
				if m.cluster.Membership != "unjoined" || m.clusterError != "" {
					m.notice = "Create cluster is disabled: this node must be unjoined with current status."
					return m, nil
				}
				m.creating = true
				return m, m.nameInput.Focus()
			}
			cmd := domain.Command{ID: domain.NewID("cmd_"), Type: "network.check", TargetID: m.network.NodeID, ExpectedVersions: map[string]int64{}, Payload: json.RawMessage(`{}`)}
			if m.taskFocus {
				if len(m.tasks) == 0 || v.String() == "enter" {
					return m, nil
				}
				task := m.tasks[m.selected]
				action := presentation.TaskActions(task)[0]
				if v.String() == "r" {
					action = presentation.TaskActions(task)[1]
				}
				if !action.Enabled {
					m.notice = action.Reason
					return m, nil
				}
				cmd.Type = action.Type
				cmd.TargetID = task.TargetID
				cmd.ExpectedVersions["task:"+task.ID] = task.Version
				cmd.Payload, _ = json.Marshal(map[string]string{"task_id": task.ID})
			} else if v.String() == "c" {
				return m, nil
			}
			return m.submit(cmd)
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}
func (m Model) submit(cmd domain.Command) (tea.Model, tea.Cmd) {
	m.pending = &cmd
	m.submitting = true
	m.notice = "Submitting " + cmd.ID
	return m, func() tea.Msg { receipt, err := m.client.Submit(m.ctx, cmd); return submitted{receipt, err} }
}
func (m *Model) content() {
	offset := m.viewport.YOffset
	lines := []string{"Connection", presentation.Network(m.network), "Freshness: " + m.network.Freshness, "", "Recent tasks"}
	if m.clusterFocus {
		lines = []string{"Cluster", "Membership: " + m.cluster.Membership}
		if m.clusterError != "" {
			lines = append(lines, m.clusterError)
		}
		if m.cluster.Name != nil {
			lines = append(lines, "Name: "+*m.cluster.Name)
		}
		if m.cluster.ClusterID != nil {
			lines = append(lines, "ID: "+*m.cluster.ClusterID, "Broker: "+m.cluster.BrokerRole, fmt.Sprintf("Quorum available: %t", m.cluster.Quorum), fmt.Sprintf("Full members: %d", m.cluster.FullMembers))
		}
		if m.cluster.Membership == "unjoined" && m.clusterError == "" {
			lines = append(lines, "c: Create cluster")
		} else {
			lines = append(lines, "Create cluster disabled: requires an unjoined node")
		}
		lines = append(lines, "", "Recent tasks")
	}
	if m.taskFocus && len(m.tasks) > 0 {
		task := m.tasks[m.selected]
		lines = []string{"Selected task", presentation.Task(task), fmt.Sprintf("Reviewed version: %d", task.Version)}
		for _, action := range presentation.TaskActions(task) {
			line := action.Label
			if !action.Enabled {
				line += " (disabled): " + action.Reason
			}
			lines = append(lines, line)
		}
		lines = append(lines, "", "Recent tasks")
	}
	for index, t := range m.tasks {
		marker := "  "
		if m.taskFocus && index == m.selected {
			marker = "> "
		}
		lines = append(lines, marker+presentation.Task(t))
	}
	if len(m.tasks) == 0 {
		lines = append(lines, "No tasks yet.")
	}
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(strings.Join(lines, "\n")))
	m.viewport.SetYOffset(offset)
}
func (m Model) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86")).Render("Nixmox")
	hint := "Network · Enter/r: Check network"
	if m.taskFocus {
		hint = "Tasks · ↑/↓: select · c: Cancel · r: Retry"
	}
	if m.clusterFocus {
		hint = "Cluster · c: Create cluster"
	}
	input := ""
	if m.creating {
		input = "\nCreate cluster: " + m.nameInput.View() + "\nEnter: submit · Esc: cancel"
	}
	return title + "\n\n" + m.viewport.View() + input + "\n\n" + lipgloss.NewStyle().Width(m.width).Render(m.notice) + "\n" + hint + " · Tab: switch · g: Cluster · ?: help · q: close\n"
}
func Run(ctx context.Context, c *client.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err := tea.NewProgram(New(ctx, c), tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	return err
}
