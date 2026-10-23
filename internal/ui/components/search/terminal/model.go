package terminal

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"nixmox/internal/ui/components/search"
	"nixmox/internal/ui/components/theme"
)

type Model struct {
	Input   textinput.Model
	Editing bool
}

func New() Model {
	input := textinput.New()
	input.Prompt = " / "
	input.Placeholder = search.Placeholder
	input.CharLimit = 128
	return Model{Input: input}
}
func (m *Model) Style(t theme.Theme, width int, reduced bool) {
	m.Input.SetWidth(max(5, width-4))
	s := m.Input.Styles()
	s.Cursor.Blink = !reduced
	s.Cursor.Color = lipgloss.Color(t.Accent)
	s.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent))
	m.Input.SetStyles(s)
}
func (m *Model) Focus() tea.Cmd { m.Editing = true; return m.Input.Focus() }
func (m *Model) Reset()         { m.Input.SetValue(""); m.Input.Blur(); m.Editing = false }
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyPressMsg); ok && (key.String() == "enter" || key.String() == "esc") {
		m.Editing = false
		m.Input.Blur()
		if key.String() == "esc" {
			m.Input.SetValue("")
		}
		return nil
	}
	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(msg)
	return cmd
}
