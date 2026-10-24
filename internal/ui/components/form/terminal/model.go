package terminal

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"nixmox/internal/ui/components/form"
)

type Model struct {
	Input  textinput.Model
	FormID string
	Field  form.Field
	Active bool
	choice int
}

func New() Model { return Model{Input: textinput.New()} }
func (m *Model) Begin(id string, field form.Field) tea.Cmd {
	m.Reset()
	m.FormID, m.Field, m.Active = id, field, true
	m.Input.Prompt = field.Label + ": "
	m.Input.CharLimit = 256 << 10
	m.Input.SetWidth(60)
	if field.Kind == "password" {
		m.Input.EchoMode = textinput.EchoPassword
	} else {
		m.Input.EchoMode = textinput.EchoNormal
		m.Input.SetValue(field.Value)
	}
	if field.Kind == "select" && len(field.Choices) > 0 {
		for i, c := range field.Choices {
			if c.Value == field.Value {
				m.choice = i
			}
		}
		m.Input.SetValue(field.Choices[m.choice].Value)
	}
	return m.Input.Focus()
}
func (m *Model) Reset() {
	m.Input.SetValue("")
	m.Input.Blur()
	m.Active = false
	m.FormID = ""
	m.Field = form.Field{}
	m.choice = 0
}
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	if m.Field.Kind == "select" {
		if key, ok := msg.(tea.KeyPressMsg); ok && len(m.Field.Choices) > 0 {
			switch key.String() {
			case "left", "up":
				m.choice = (m.choice + len(m.Field.Choices) - 1) % len(m.Field.Choices)
			case "right", "down":
				m.choice = (m.choice + 1) % len(m.Field.Choices)
			}
			m.Input.SetValue(m.Field.Choices[m.choice].Value)
		}
		return nil
	}
	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(msg)
	return cmd
}
func (m *Model) View() string {
	if m.Field.Kind == "select" && len(m.Field.Choices) > 0 {
		return m.Field.Label + ": ◀ " + m.Field.Choices[m.choice].Label + " ▶  Enter to select"
	}
	return m.Input.View()
}
