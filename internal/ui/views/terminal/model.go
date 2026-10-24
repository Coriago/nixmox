package terminal

import (
	"context"
	"nixmox/internal/ui"
	"slices"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	confirmation "nixmox/internal/ui/components/confirmation/terminal"
	logs "nixmox/internal/ui/components/log/terminal"
	"nixmox/internal/ui/components/progress"
	search "nixmox/internal/ui/components/search/terminal"
	"nixmox/internal/ui/views"
)

type frame time.Time
type Model struct {
	connection    *views.Connection
	ctx           context.Context
	updates       <-chan views.Update
	selection     views.Selection
	Core          *views.Model
	view          views.View
	width, height int
	content       viewport.Model
	search        search.Model
	focus         views.Action
	logView       logs.View
	modal         confirmation.State
	last          time.Time
}

func New(core *views.Model) *Model {
	m := &Model{Core: core, width: 120, height: 40, content: viewport.New(), search: search.New(), logView: logs.View{FollowState: &core.LogFollow}}
	m.resize()
	m.sync()
	return m
}
func tick() tea.Cmd {
	return tea.Tick(progress.FrameInterval, func(t time.Time) tea.Msg { return frame(t) })
}
func (m *Model) Init() tea.Cmd { return tea.Batch(tick(), m.waitUpdate()) }
func (m *Model) sidebarWidth() int {
	if m.width < 85 {
		return 19
	}
	return 25
}
func (m *Model) resize() {
	m.content.SetWidth(max(10, m.width-m.sidebarWidth()-4))
	m.content.SetHeight(max(3, m.height-8))
}
func (m *Model) sync() {
	m.view = m.Core.View()
	m.search.Style(m.view.Theme, m.content.Width(), m.Core.Reduced)
	m.content.SetContent(m.contentPage().text)
	if m.view.Find(views.Logs) != nil {
		m.logView.Sync(&m.content)
	}
}
func (m *Model) activate(a views.Action) {
	if a.Kind == "search-focus" && m.Core.Modal == nil {
		if m.view.Find(views.Search) == nil {
			return
		}
		m.search.Focus()
		m.sync()
		return
	}
	old := m.Core.Route()
	hadModal := m.Core.Modal != nil
	m.Core.Dispatch(a)
	m.exchange()
	if !hadModal && m.Core.Modal != nil {
		m.modal = confirmation.State{}
	}
	if old[0] != m.Core.Page || a.Kind == "page" {
		m.search.Reset()
	}
	if old != m.Core.Route() {
		m.content.GotoTop()
	}
	m.resize()
	m.sync()
}
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.search.Editing {
		switch msg.(type) {
		case frame, tea.WindowSizeMsg, views.Update:
		default:
			if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
				return m, tea.Quit
			}
			cmd := m.search.Update(msg)
			m.Core.Dispatch(views.Action{Kind: "search", ID: m.search.Input.Value()})
			m.sync()
			return m, cmd
		}
	}
	switch msg := msg.(type) {
	case views.Update:
		m.Core.Apply(msg)
		m.exchange()
		m.sync()
		return m, m.waitUpdate()
	case frame:
		delta := progress.FrameInterval
		if !m.last.IsZero() {
			delta = time.Time(msg).Sub(m.last)
		}
		m.last = time.Time(msg)
		m.Core.Advance(delta)
		if m.Core.Modal != nil {
			m.modal.Frame(m.Core.Reduced)
		}
		m.sync()
		return m, tick()
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		m.sync()
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		_, targets := m.layout()
		for _, t := range targets {
			if msg.X >= t.x && msg.X < t.x+t.w && msg.Y >= t.y && msg.Y < t.y+t.h && (!t.content || (msg.Y >= 4 && msg.Y < m.height-4)) {
				m.focus = t.action
				m.activate(t.action)
				break
			}
		}
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.Core.Modal != nil {
			if a := m.modal.Key(key); a.Kind != "" {
				m.activate(a)
			}
			return m, nil
		}
		if a, ok := views.Shortcut(key); ok {
			if a.Kind == "search-focus" && m.view.Find(views.Search) != nil {
				return m, m.search.Focus()
			}
			m.activate(a)
			return m, nil
		}
		switch key {
		case "q":
			return m, tea.Quit
		case "enter":
			m.activate(m.focus)
		case "j", "k", "tab", "shift+tab":
			_, targets := m.layout()
			if key == "j" || key == "k" {
				targets = slices.DeleteFunc(targets, func(t target) bool { return !t.content || !t.row })
			}
			step := 1
			if key == "k" || key == "shift+tab" {
				step = -1
			}
			m.moveFocus(targets, step)
		default:
			return m, m.scroll(msg)
		}
	case tea.MouseWheelMsg:
		if m.Core.Modal != nil {
			return m, nil
		}
		if msg.X >= m.sidebarWidth()+2 && msg.Y >= 4 && msg.Y < m.height-4 {
			return m, m.scroll(msg)
		}
	}
	return m, nil
}
func (m *Model) View() tea.View { v, _ := m.layout(); return v }

func (m *Model) scroll(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.content, cmd = m.content.Update(msg)
	if m.view.Find(views.Logs) != nil {
		m.logView.Scrolled(m.content)
	}
	return cmd
}

func (m *Model) moveFocus(targets []target, step int) {
	i := slices.IndexFunc(targets, func(t target) bool { return t.action == m.focus })
	i = ui.Cycle(i, step, len(targets))
	if i < 0 {
		return
	}
	t := targets[i]
	m.focus = t.action
	if t.content {
		line := t.y - 4 + m.content.YOffset()
		if line < m.content.YOffset() {
			m.content.SetYOffset(line)
		}
		if line+t.h > m.content.YOffset()+m.content.Height() {
			m.content.SetYOffset(line + t.h - m.content.Height())
		}
	}
	m.sync()
}
