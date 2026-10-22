package terminal

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"nixmox/internal/ui/progress"
)

func TestAdapterUsesController(t *testing.T) {
	d := progress.NewDemo()
	m := New(progress.NewDemo(), io.Discard)
	cases := []struct {
		key    tea.KeyMsg
		action string
	}{
		{tea.KeyMsg{Type: tea.KeyDown}, progress.Next},
		{tea.KeyMsg{Type: tea.KeyEnter}, progress.Expand},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")}, progress.Pause},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")}, progress.Completed},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")}, progress.Motion},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}, progress.Speed},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}, progress.Failure},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}, progress.Restart},
	}
	for _, test := range cases {
		m.Update(test.key)
		d.Dispatch(progress.Action{Kind: test.action})
		if !reflect.DeepEqual(m.Demo.View(), d.View()) {
			t.Fatal("adapter diverged", test.action)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	d.Dispatch(progress.Action{Kind: progress.Pause})
	now := time.Unix(1, 0)
	for range 30 {
		m.Update(Tick(now))
		d.Advance(progress.FrameInterval)
		now = now.Add(progress.FrameInterval)
	}
	if !reflect.DeepEqual(m.Demo.View(), d.View()) {
		t.Fatal("tick semantics diverged")
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 15}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View()
		for _, line := range strings.Split(strings.TrimSuffix(view, "\n"), "\n") {
			if lipgloss.Width(line) > size[0] {
				t.Fatal("horizontal overflow")
			}
		}
		if lipgloss.Height(strings.TrimSuffix(view, "\n")) > size[1] {
			t.Fatal("vertical overflow")
		}
	}
}
