package terminal

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"nixmox/internal/ui/views"
)

func TestOperationalLayouts(t *testing.T) {
	m := New(views.New())
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 44})
	m.activate(views.Action{Kind: "page", ID: "Tasks"})
	if strings.Contains(ansi.Strip(m.View().Content), "LIVE INSPECTOR") {
		t.Fatal("terminal-only content bypassed the shared layout")
	}
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	first := m.focus
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.focus == first || m.focus.Kind != "task" {
		t.Fatal("row selection did not change focus")
	}
	m.activate(views.Action{Kind: "host", ID: "birch"})
	view := ansi.Strip(m.View().Content)
	t.Log("Host console layout:\n" + view)
	for _, label := range []string{"Shutdown", "Processor", "TELEMETRY", "TASK HISTORY", "Network check"} {
		if !strings.Contains(view, label) {
			t.Fatal("host operations not visible", label)
		}
	}
	for _, size := range [][2]int{{150, 44}, {120, 40}, {80, 24}, {52, 18}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		checkBounds(t, m, size)
	}
}
func TestListSearch(t *testing.T) {
	m := New(views.New())
	m.activate(views.Action{Kind: "page", ID: "Hosts"})
	m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	for _, r := range "birch" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.Core.Query != "birch" || len(m.Core.View().Find(views.Tables).Table.Records) != 1 {
		t.Fatal("search did not filter inventory")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.Core.Query != "" || m.search.Editing {
		t.Fatal("Escape did not clear search")
	}
}
