// Package views owns shared page definitions, state and actions, never DOM or terminal IO.
package views

import (
	"slices"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/ui"
	"nixmox/internal/ui/components/confirmation"
	"nixmox/internal/ui/components/form"
	logs "nixmox/internal/ui/components/log"
	"nixmox/internal/ui/components/progress"
	"nixmox/internal/ui/components/theme"
)

type Action = ui.Action
type Model struct {
	Page, HostID, TaskID, Tab, Filter, Query string
	ThemeIndex, AccentIndex                  int
	Reduced                                  bool
	Modal                                    *confirmation.Model
	Form                                     *form.Model
	formKind                                 string
	formSecret                               []byte
	formRepository                           domain.Repository
	formCredentialID                         domain.ID
	secretSubmission                         *CredentialSubmission
	Cluster                                  Cluster
	Notice                                   string
	Hosts                                    []Host
	Discoveries                              []Discovery
	Tasks                                    []Task
	Credentials                              []domain.Credential
	Repositories                             []domain.Repository
	LogFollow                                logs.FollowState
	accumulator                              time.Duration
	frame                                    uint64
	bars                                     map[string]*progress.Spring
	identity                                 domain.Identity
	connected                                bool
	connectionError                          string
	warnings                                 string
	telemetry                                map[string]domain.HostTelemetry
	histories                                map[string][]float64
	logOffsets                               map[string]int64
	logPartial                               map[string]string
	review                                   *domain.Command
	command                                  *domain.Command
	busy                                     bool
	uncertain                                domain.ID
}

func New() *Model {
	m := &Model{Page: Pages[0].Name, Tab: "Summary", Filter: "All", bars: map[string]*progress.Spring{}, telemetry: map[string]domain.HostTelemetry{}, histories: map[string][]float64{}, logOffsets: map[string]int64{}, logPartial: map[string]string{}}
	m.LogFollow.Follow = true
	return m
}
func (m *Model) Theme() theme.Theme {
	t := theme.Themes[m.ThemeIndex]
	if m.AccentIndex > 0 {
		t.Accent = []string{"", "#f18a24", "#d96a00", "#efa345"}[m.AccentIndex]
	}
	return t
}
func (m *Model) Host(id string) *Host {
	return ui.Find(m.Hosts, func(h Host) bool { return h.ID == id })
}
func (m *Model) Task(id string) *Task {
	return ui.Find(m.Tasks, func(t Task) bool { return t.ID == id })
}
func (m *Model) discovery(id string) *Discovery {
	return ui.Find(m.Discoveries, func(d Discovery) bool { return d.ID == id || string(d.NodeID) == id })
}
func (m *Model) Route() [4]string { return [4]string{m.Page, m.HostID, m.TaskID, m.Tab} }
func (m *Model) Dispatch(a Action) {
	old := m.Route()
	defer func() {
		if old != m.Route() {
			m.LogFollow.Follow = true
		}
	}()
	if m.Modal != nil {
		switch a.Kind {
		case "dismiss":
			m.Modal, m.review = nil, nil
		case "confirm":
			m.command, m.review = m.review, nil
			m.busy = m.command != nil
			m.Modal = nil
		}
		return
	}
	switch a.Kind {
	case "credential-import", "repository-import", "repository-bind":
		m.openSourceForm(a.Kind, a.ID)
	case "source-form-close":
		m.closeSourceForm()
	case "source-form-save":
		m.submitSourceForm()
	case "page":
		m.closeSourceForm()
		if slices.ContainsFunc(Pages, func(p Page) bool { return p.Name == a.ID }) {
			m.Page = a.ID
			m.Query = ""
			m.HostID = ""
			m.TaskID = ""
			m.Tab = "Summary"
		}
	case "host":
		if m.Host(a.ID) != nil {
			if m.HostID == "" {
				m.Tab = "Summary"
			}
			m.Page = "Hosts"
			m.HostID = a.ID
			m.TaskID = ""
		}
	case "task":
		if m.Task(a.ID) != nil {
			m.Page = "Tasks"
			m.TaskID = a.ID
			m.HostID = ""
			m.Tab = "Overview"
		}
	case "back":
		m.HostID = ""
		m.TaskID = ""
		m.Tab = "Summary"
	case "tab":
		if tabs := m.View().Find(Tabs); tabs != nil {
			for _, tab := range tabs.Buttons {
				if tab.Action.Kind == "tab" && tab.Action.ID == a.ID {
					m.Tab = a.ID
					break
				}
			}
		}
	case "follow":
		if m.View().Find(Logs) != nil {
			m.LogFollow.Follow = true
		}
	case "search":
		if len(a.ID) <= 128 {
			m.Query = ui.Text(a.ID, 128, -1)
		}
	case "filter":
		if slices.Contains([]string{"All", "Active", "Needs attention"}, a.ID) {
			m.Filter = a.ID
		}
	case "theme":
		m.ThemeIndex = (m.ThemeIndex + 1) % len(theme.Themes)
	case "accent":
		m.AccentIndex = (m.AccentIndex + 1) % 4
	case "motion":
		m.Reduced = !m.Reduced
	case "refresh", "network-check", "shutdown", "cancel", "approve", "reject", "add", "credential-check", "credential-remove", "repository-fetch":
		m.prepare(a)
	case "reconcile":
		if m.uncertain != "" && !m.busy {
			m.command = &domain.Command{ID: m.uncertain}
			m.busy = true
		}
	case "help":
		m.Modal = &confirmation.Model{Title: "Keyboard and mouse", Body: "Tab / Shift-Tab focus · Enter activate · Esc back · ↑ ↓ scroll · 1–9 pages · / filter · t theme · m motion · q quit TUI.", Confirm: "Got it"}
	}
}

// Shortcut returns the same presentation action in the DOM and terminal.
func Shortcut(key string) (Action, bool) {
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		i := int(key[0] - '1')
		if i < len(Pages) {
			return Action{Kind: "page", ID: Pages[i].Name}, true
		}
	}
	bindings := map[string]string{"/": "search-focus", "t": "theme", "m": "motion", "?": "help", "Escape": "back", "esc": "back"}
	kind, ok := bindings[key]
	return Action{Kind: kind}, ok
}

func (m *Model) Advance(dt time.Duration) {
	for _, sample := range m.telemetry {
		if !liveSample(sample) {
			m.projectTelemetry(sample, false)
		}
	}
	// Fixed timestep bounds background-tab catch-up; never replay an unbounded backlog.
	dt = min(max(dt, 0), 250*time.Millisecond)
	m.accumulator += dt
	for m.accumulator >= progress.FrameInterval {
		m.accumulator -= progress.FrameInterval
		if !m.Reduced {
			m.frame++
		}
		for i := range m.Tasks {
			t := &m.Tasks[i]
			s := m.bars[t.ID]
			if s == nil {
				s = &progress.Spring{}
				m.bars[t.ID] = s
			}
			target := 0.0
			if t.Total > 0 {
				target = float64(t.Done) / float64(t.Total)
			}
			s.Step(target, m.Reduced)
		}
	}
}
