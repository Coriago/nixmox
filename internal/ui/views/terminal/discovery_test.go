package terminal

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"nixmox/internal/ui/views"
)

func TestDiscoveryButtonsKeyboardAndMouse(t *testing.T) {
	for _, size := range [][2]int{{150, 44}, {80, 24}, {52, 18}} {
		m := New(views.New())
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		if m.Core.Page != "Cluster" {
			t.Fatal("wrong terminal landing page")
		}
		_, targets := m.layout()
		actions := map[views.Action]target{}
		for _, target := range targets {
			actions[target.action] = target
		}
		approve := views.Action{Kind: "approve", ID: "maple"}
		for _, action := range []views.Action{approve, {Kind: "reject", ID: "maple"}, {Kind: "add", ID: "spruce"}} {
			if _, ok := actions[action]; !ok {
				t.Fatal("missing focusable discovery action", action)
			}
		}
		target := actions[approve]
		m.content.SetYOffset(target.y - 4 + m.content.YOffset())
		_, targets = m.layout()
		for _, target = range targets {
			if target.action == approve {
				break
			}
		}
		m.Update(tea.MouseClickMsg{X: target.x + 1, Y: target.y, Button: tea.MouseLeft})
		if m.Core.Modal == nil || m.Core.Modal.ResourceID != "maple" {
			t.Fatal("discovery mouse action opened wrong review")
		}
		checkBounds(t, m, size)
		m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if h := m.Core.Host("maple"); h == nil || h.Membership != "pending" {
			t.Fatal("keyboard confirmation failed")
		}
		m.activate(views.Action{Kind: "page", ID: "Settings"})
		checkBounds(t, m, size)
		sections := 0
		for _, b := range m.Core.View().Blocks {
			if b.Kind == views.Sections {
				sections++
			}
		}
		if m.Core.View().Find(views.Tabs) != nil || sections != 4 {
			t.Fatal("terminal settings not flattened")
		}
	}
}
