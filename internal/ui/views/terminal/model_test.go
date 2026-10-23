package terminal

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"nixmox/internal/ui/views"
)

func TestFullscreenLayoutAndInputs(t *testing.T) {
	m := New(views.New())
	for _, size := range [][2]int{{140, 45}, {100, 35}, {80, 24}, {52, 18}, {40, 12}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, p := range views.Pages {
			m.activate(views.Action{Kind: "page", ID: p.Name})
			checkBounds(t, m, size)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	m.Update(tea.MouseClickMsg{X: 4, Y: 9, Button: tea.MouseLeft})
	if m.Core.Page != "Tasks" {
		t.Fatal("navigation mouse click not routed")
	}
	m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	if m.Core.ThemeIndex != 1 {
		t.Fatal("theme shortcut missing")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.focus.Kind == "" {
		t.Fatal("no keyboard focus")
	}
}
func checkBounds(t *testing.T, m *Model, size [2]int) {
	t.Helper()
	view := m.View()
	if !view.AltScreen || view.BackgroundColor == nil {
		t.Fatal("not a full background-painted alternate screen")
	}
	for _, line := range strings.Split(view.Content, "\n") {
		if lipgloss.Width(line) > size[0] {
			t.Fatalf("%s %v horizontal overflow %d: %q", m.Core.Page, size, lipgloss.Width(line), ansi.Strip(line))
		}
	}
	if lipgloss.Height(view.Content) > size[1] {
		t.Fatalf("%s %v vertical overflow %d", m.Core.Page, size, lipgloss.Height(view.Content))
	}
}
func TestModalFocusAndMouse(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {52, 18}} {
		m := New(views.New())
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		count := len(m.Core.Tasks)
		m.activate(views.Action{Kind: "host", ID: "birch"})
		m.activate(views.Action{Kind: "shutdown"})
		checkBounds(t, m, size)
		if m.modal.Choice != 0 {
			t.Fatal("unsafe default")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.Core.Modal != nil || len(m.Core.Tasks) != count {
			t.Fatal("default did not cancel")
		}
		m.activate(views.Action{Kind: "shutdown"})
		v, targets := m.layout()
		if len(targets) != 2 {
			t.Fatal("modal did not trap input")
		}
		confirm := targets[1]
		line := strings.Split(v.Content, "\n")[confirm.y]
		if !strings.Contains(ansi.Strip(line), "Simulate shutdown") {
			t.Fatalf("modal hitbox misaligned: %q", ansi.Strip(line))
		}
		m.Update(tea.MouseClickMsg{X: confirm.x + 1, Y: confirm.y, Button: tea.MouseLeft})
		if len(m.Core.Tasks) != count+1 {
			t.Fatal("modal confirmation not clickable")
		}
	}
}
func TestLogFollowPausesAndResumes(t *testing.T) {
	m := New(views.New())
	m.activate(views.Action{Kind: "task", ID: "network-birch"})
	m.activate(views.Action{Kind: "tab", ID: "Logs"})
	for range 60 {
		m.Core.Task("network-birch").Log.Append("a retained log line")
	}
	m.sync()
	m.Update(tea.MouseWheelMsg{X: 40, Y: 10, Button: tea.MouseWheelUp})
	if m.logView.Follow {
		t.Fatal("scroll did not pause follow")
	}
	offset := m.content.YOffset()
	m.Core.Task("network-birch").Log.Append("new output")
	m.sync()
	if offset != m.content.YOffset() {
		t.Fatal("new logs stole scroll position")
	}
	m.activate(views.Action{Kind: "follow"})
	if !m.logView.Follow || !m.content.AtBottom() {
		t.Fatal("follow did not resume")
	}
}
