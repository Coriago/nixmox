// Package tui contains terminal rendering only; all actions use the console API.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

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
	requests   []domain.JoinRequest
}
type submitted struct {
	receipt domain.Receipt
	err     error
}
type event struct{ err error }
type Model struct {
	sources             *sourceModel
	sourcesFocus        bool
	client              *client.Client
	ctx                 context.Context
	events              <-chan error
	viewport            viewport.Model
	network             domain.NetworkStatus
	tasks               []domain.Task
	notice              string
	pending             *domain.Command
	submitting          bool
	width               int
	taskFocus           bool
	selected            int
	fetching            bool
	refreshAgain        bool
	cluster             domain.ClusterStatus
	clusterError        string
	clusterFocus        bool
	creating            bool
	nameInput           textinput.Model
	addressInput        textinput.Model
	joining             bool
	requestSelected     int
	requests            []domain.JoinRequest
	review              *domain.JoinRequest
	telemetryFocus      bool
	telemetry           domain.HostTelemetry
	telemetryEvents     <-chan client.TelemetryUpdate
	telemetryCancel     context.CancelFunc
	telemetryGeneration uint64
	telemetryHistory    []telemetryPoint
}

func New(ctx context.Context, c *client.Client) Model {
	name := textinput.New()
	name.Placeholder = "Cluster name"
	name.CharLimit = 64
	name.Width = 40
	address := textinput.New()
	address.Placeholder = "Machine address:63175"
	address.CharLimit = 270
	address.Width = 40
	return Model{client: c, ctx: ctx, events: c.Events(ctx), viewport: viewport.New(76, 18), width: 80, notice: "Connecting…", fetching: true, nameInput: name, addressInput: address}
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
		var requests []domain.JoinRequest
		if clusterErr == nil && cluster.ClusterID != nil && cluster.BrokerRole != "leaf" {
			page, requestErr := m.client.JoinRequests(m.ctx, url.Values{"state": {"awaiting_approval"}, "limit": {"100"}})
			requests = page.Items
			if requestErr != nil {
				clusterErr = requestErr
			}
		}
		return snapshot{network: n, tasks: p.Items, err: err, cluster: cluster, clusterErr: clusterErr, requests: requests}
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
	if _, ok := msg.(sourceMessage); ok && m.sources != nil {
		return m, m.sources.update(msg)
	}
	if m.sourcesFocus {
		if key, ok := msg.(tea.KeyMsg); ok {
			if key.String() == "ctrl+c" {
				return m, tea.Quit
			}
			if key.String() == "esc" {
				m.sourcesFocus = false
				return m, nil
			}
			return m, m.sources.update(msg)
		}
		if _, ok := msg.(tea.WindowSizeMsg); ok {
			m.sources.update(msg)
		}
	}
	if update, ok := msg.(telemetryMessage); ok {
		if !m.telemetryFocus || update.host != m.telemetry.HostID || update.generation != m.telemetryGeneration {
			return m, nil
		}
		if update.update.Error != nil {
			m.notice = update.update.Error.Error()
			m.telemetry.Freshness = "stale"
			m.telemetry.Filesystems.Freshness = "stale"
			if len(m.telemetryHistory) > 0 && m.telemetryHistory[len(m.telemetryHistory)-1].value != nil {
				m.telemetryHistory = append(m.telemetryHistory, telemetryPoint{at: time.Now()})
			}
		} else if update.update.Sample.SchemaVersion == 1 {
			value := update.update.Sample
			if value.StreamID != m.telemetry.StreamID || value.Sequence != m.telemetry.Sequence {
				if value.ObservedAt != nil {
					if value.StreamID != m.telemetry.StreamID {
						m.telemetryHistory = append(m.telemetryHistory, telemetryPoint{at: *value.ObservedAt})
					}
					m.telemetryHistory = append(m.telemetryHistory, telemetryPoint{at: *value.ObservedAt, value: value.CPU.Utilization})
				}
				cutoff := time.Now().Add(-5 * time.Minute)
				for len(m.telemetryHistory) > 0 && (m.telemetryHistory[0].at.Before(cutoff) || len(m.telemetryHistory) > 1201) {
					m.telemetryHistory = m.telemetryHistory[1:]
				}
			}
			m.telemetry = value
			m.notice = "Host telemetry · " + value.Freshness
		}
		m.content()
		return m, m.waitTelemetry()
	}
	if key, ok := msg.(tea.KeyMsg); ok && m.telemetryFocus {
		switch key.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "h", "tab", "g":
			m.telemetryCancel()
			m.telemetryFocus = false
			m.content()
			return m, nil
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	if key, ok := msg.(tea.KeyMsg); ok && m.creating {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.creating = false
			m.joining = false
			m.addressInput.Blur()
			m.nameInput.Blur()
			return m, nil
		case "enter":
			if m.cluster.Membership != "unjoined" || m.clusterError != "" {
				m.notice = "Cluster creation requires a current unjoined node."
				return m, nil
			}
			payload, _ := json.Marshal(map[string]string{"name": m.nameInput.Value()})
			kind := "cluster.create"
			if m.joining {
				kind = "cluster.join"
				payload, _ = json.Marshal(map[string]string{"address": m.addressInput.Value(), "expected_cluster_name": m.nameInput.Value()})
			}
			m.creating = false
			m.joining = false
			m.addressInput.Blur()
			m.nameInput.Blur()
			return m.submit(domain.Command{ID: domain.NewID("cmd_"), Type: kind, TargetID: m.network.NodeID, Payload: payload, ExpectedVersions: map[string]int64{}})
		case "tab", "shift+tab":
			if m.joining {
				if m.addressInput.Focused() {
					m.addressInput.Blur()
					return m, m.nameInput.Focus()
				}
				m.nameInput.Blur()
				return m, m.addressInput.Focus()
			}
			return m, nil
		default:
			var cmd tea.Cmd
			if m.joining && m.addressInput.Focused() {
				m.addressInput, cmd = m.addressInput.Update(msg)
			} else {
				m.nameInput, cmd = m.nameInput.Update(msg)
			}
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
		requestID := ""
		if m.requestSelected < len(m.requests) {
			requestID = m.requests[m.requestSelected].ID
		}
		m.requests = v.requests
		m.requestSelected = 0
		for i, r := range m.requests {
			if r.ID == requestID {
				m.requestSelected = i
			}
		}
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
		case "h":
			host := m.network.NodeID
			if m.taskFocus && len(m.tasks) > m.selected {
				host = m.tasks[m.selected].OwnerNodeID
			}
			if host == "" {
				return m, nil
			}
			ctx, cancel := context.WithCancel(m.ctx)
			m.telemetryCancel = cancel
			m.telemetryGeneration++
			m.telemetryFocus = true
			m.telemetry = domain.UnknownTelemetry(host)
			m.telemetryHistory = nil
			m.telemetryEvents = m.client.TelemetryEvents(ctx, host)
			m.content()
			m.viewport.GotoTop()
			return m, m.waitTelemetry()
		case "esc":
			if m.review != nil {
				m.review = nil
				m.content()
				return m, nil
			}
			return m, tea.Quit
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?":
			m.notice = "Tab: Network/Tasks · g: Cluster · Enter/r: check · ↑/↓: select task · c: cancel/create · r: retry · p: retry uncertain submission · PgUp/PgDn: scroll · q: close"
			return m, nil
		case "g":
			m.sourcesFocus = false
			m.clusterFocus = !m.clusterFocus
			m.taskFocus = false
			m.content()
			m.viewport.GotoTop()
			return m, nil
		case "s":
			m.sourcesFocus = true
			if m.sources == nil {
				m.sources = newSources(m.ctx, m.client, m.width, m.viewport.Height+8)
				return m, m.sources.fetch()
			}
			return m, nil
		case "tab", "shift+tab":
			m.clusterFocus = false
			m.taskFocus = !m.taskFocus
			m.content()
			m.viewport.GotoTop()
			return m, nil
		case "up", "down":
			if m.clusterFocus && m.review == nil && len(m.requests) > 0 {
				if v.String() == "up" {
					m.requestSelected = max(0, m.requestSelected-1)
				} else {
					m.requestSelected = min(len(m.requests)-1, m.requestSelected+1)
				}
				m.content()
				return m, nil
			}
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
		case "enter", "r", "c", "j", "a", "x":
			if m.submitting || m.network.NodeID == "" {
				return m, nil
			}
			if m.pending != nil {
				m.notice = "An uncertain submission is pending. Press p to retry its exact command."
				return m, nil
			}
			if m.clusterFocus {
				if m.review != nil && (v.String() == "a" || v.String() == "x") {
					if m.clusterError != "" || m.cluster.IssuerNodeID == nil || m.review.State != "awaiting_approval" {
						m.notice = "A current cluster and awaiting request are required."
						return m, nil
					}
					kind := "cluster.request.approve"
					if v.String() == "x" {
						kind = "cluster.request.reject"
					}
					payload, _ := json.Marshal(map[string]string{"request_id": m.review.ID})
					return m.submit(domain.Command{ID: domain.NewID("cmd_"), Type: kind, TargetID: *m.cluster.IssuerNodeID, ExpectedVersions: map[string]int64{"join_request:" + m.review.ID: m.review.Version}, Payload: payload})
				}
				if v.String() == "enter" && len(m.requests) > 0 {
					r := m.requests[m.requestSelected]
					m.review = &r
					m.content()
					m.viewport.GotoTop()
					return m, nil
				}
				if v.String() != "c" && v.String() != "j" {
					return m, nil
				}
				if m.cluster.Membership != "unjoined" || m.clusterError != "" {
					m.notice = "Create cluster is disabled: this node must be unjoined with current status."
					return m, nil
				}
				m.creating = true
				m.joining = v.String() == "j"
				if m.joining {
					m.nameInput.Blur()
					return m, m.addressInput.Focus()
				}
				return m, m.nameInput.Focus()
			}
			if v.String() == "a" || v.String() == "x" || v.String() == "j" {
				return m, nil
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
	if m.telemetryFocus {
		m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(m.telemetryContent()))
		m.viewport.SetYOffset(offset)
		return
	}
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
			lines = append(lines, "ID: "+*m.cluster.ClusterID, "Broker: "+m.cluster.BrokerRole, fmt.Sprintf("Quorum available: %t (%d of %d required)", m.cluster.Quorum.Available, m.cluster.Quorum.Required, m.cluster.Quorum.Replicas), "Health: "+m.cluster.Health.Status, fmt.Sprintf("Full members: %d", m.cluster.FullMembers))
		}
		if m.cluster.Membership == "unjoined" && m.clusterError == "" {
			lines = append(lines, "c: Create cluster · j: Join cluster")
		} else {
			lines = append(lines, "Create cluster disabled: requires an unjoined node")
		}
		if m.review != nil {
			r := m.review
			lines = append(lines, "", "Review join request", r.Name, "Node: "+r.NodeID, "Key: "+r.KeyFingerprint, fmt.Sprintf("Reviewed version: %d · %s", r.Version, r.State), "Expires: "+r.ExpiresAt.String(), "a: Approve pending membership · x: Reject · Esc: Back")
		} else if len(m.requests) > 0 {
			lines = append(lines, "", "Join requests · ↑/↓: Select · Enter: Review")
			for i, r := range m.requests {
				marker := "  "
				if i == m.requestSelected {
					marker = "> "
				}
				lines = append(lines, marker+r.Name+" · "+r.NodeID)
			}
		}
		lines = append(lines, "", "Recent tasks")
	}
	if m.taskFocus && len(m.tasks) > 0 {
		task := m.tasks[m.selected]
		lines = []string{"Selected task", presentation.Task(task), fmt.Sprintf("Reviewed version: %d", task.Version)}
		if result := presentation.TaskResult(task); result != "" {
			lines = append(lines, "Result", result)
		}
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
	if m.sourcesFocus && m.sources != nil {
		return m.sources.view()
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86")).Render("Nixmox")
	hint := "Network · Enter/r: Check network"
	if m.taskFocus {
		hint = "Tasks · ↑/↓: select · c: Cancel · r: Retry"
	}
	if m.clusterFocus {
		hint = "Cluster · c: Create · j: Join · Enter: Review request"
	}
	if m.telemetryFocus {
		hint = "Host telemetry · Esc/h: Back · PgUp/PgDn: Scroll"
	}
	input := ""
	if m.creating {
		input = "\nCreate cluster: " + m.nameInput.View() + "\nEnter: submit · Esc: cancel"
		if m.joining {
			input = "\nJoin cluster\nAddress: " + m.addressInput.View() + "\nExpected name: " + m.nameInput.View() + "\nTab: switch field · Enter: request join · Esc: cancel"
		}
	}
	return title + "\n\n" + m.viewport.View() + input + "\n\n" + lipgloss.NewStyle().Width(m.width).Render(m.notice) + "\n" + hint + " · Tab: switch · g: Cluster · s: Sources · h: Telemetry · ?: help · q: close\n"
}
func Run(ctx context.Context, c *client.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err := tea.NewProgram(New(ctx, c), tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	return err
}
