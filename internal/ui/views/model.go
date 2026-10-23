// Package views owns shared page definitions, state and actions, never DOM or terminal IO.
// Mock observations are deliberately isolated in mock.go.
package views

import (
	"slices"
	"time"

	"nixmox/internal/ui"
	"nixmox/internal/ui/components/confirmation"
	logs "nixmox/internal/ui/components/log"
	"nixmox/internal/ui/components/progress"
	"nixmox/internal/ui/components/theme"
)

type Action = ui.Action
type Host struct {
	ID, Name, Address, Reachability, Membership, Role string
	CPU                                               float64
	Memory                                            string
	Architecture, Processor, OS, Uptime               string
	Cores                                             int
	MemoryGiB, MemoryUsedGiB                          float64
	History                                           []float64
}
type Task struct {
	ID, Name, HostID, State, Phase, Result, Started string
	Done, Total                                     int
	Age                                             time.Duration
	Log                                             logs.Model
}
type Model struct {
	Page, HostID, TaskID, Tab, Filter, Query string
	ThemeIndex, AccentIndex                  int
	Reduced, Paused                          bool
	Modal                                    *confirmation.Model
	Notice                                   string
	Hosts                                    []Host
	Discoveries                              []Discovery
	Tasks                                    []Task
	elapsed, accumulator                     time.Duration
	frame                                    uint64
	bars                                     map[string]*progress.Spring
}

func New() *Model {
	m := &Model{Page: Pages[0].Name, Tab: "Summary", Filter: "All", bars: map[string]*progress.Spring{}}
	m.resetMock()
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
func (m *Model) Route() [4]string { return [4]string{m.Page, m.HostID, m.TaskID, m.Tab} }
func (m *Model) Dispatch(a Action) {
	if m.Modal != nil {
		switch a.Kind {
		case "dismiss":
			m.Modal = nil
		case "confirm":
			m.confirmMock(*m.Modal)
			m.Modal = nil
		}
		return
	}
	switch a.Kind {
	case "page":
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
	case "pause":
		m.Paused = !m.Paused
	case "replay":
		m.resetMock()
		m.Notice = "Mock scenario restarted. No host operations."
	case "refresh", "network-check":
		m.hostActionMock(a.Kind)
	case "shutdown":
		if h := m.Host(m.HostID); h != nil && h.Reachability == "online" {
			m.Modal = &confirmation.Model{Title: "Schedule shutdown?", Target: h.Name, Body: "This is a simulated power action. The preview will add a reconciling task, not contact the host.", Confirm: "Simulate shutdown", Action: "shutdown"}
		}
	case "cancel":
		if t := m.Task(m.TaskID); t != nil && t.State == "running" {
			m.Modal = &confirmation.Model{Title: "Cancel this task?", Target: t.Name, Body: "Stop the simulated execution. Its existing log remains available.", Confirm: "Cancel task", Action: "cancel"}
		}
	case "approve", "reject", "add":
		m.reviewDiscovery(a)
	case "help":
		m.Modal = &confirmation.Model{Title: "Make yourself at home", Target: "Keyboard + mouse", Body: "Tab / Shift-Tab focus · Enter activate · Esc back · ↑ ↓ scroll · 1–9 pages · t theme · m motion · p pause demo · q quit TUI. All actions are simulated.", Confirm: "Got it", Action: "help"}
	}
}
func (m *Model) Advance(dt time.Duration) {
	// Fixed timestep bounds background-tab catch-up; never replay an unbounded backlog.
	dt = min(max(dt, 0), 250*time.Millisecond)
	m.accumulator += dt
	for m.accumulator >= progress.FrameInterval {
		m.accumulator -= progress.FrameInterval
		if !m.Reduced {
			m.frame++
		}
		if !m.Paused {
			m.elapsed += progress.FrameInterval
			m.advanceMock()
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
