package terminal

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"nixmox/internal/ui/components/form"
)

type Model struct {
	Input  textinput.Model
	Text   textarea.Model
	FormID string
	Field  form.Field
	Active bool
	choice int
}

func New() Model { return Model{Input: textinput.New(), Text: textarea.New()} }
func (m *Model) Begin(id string, field form.Field) tea.Cmd {
	m.Reset()
	m.FormID, m.Field, m.Active = id, field, true
	if field.Kind == "json" {
		m.Text.CharLimit = form.InputLimit
		m.Text.SetWidth(65)
		m.Text.SetHeight(12)
		m.Text.SetValue(field.Value)
		return m.Text.Focus()
	}
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
	m.Text.SetValue("")
	m.Text.Blur()
	m.Active = false
	m.FormID = ""
	m.Field = form.Field{}
	m.choice = 0
}
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	if m.Field.Kind == "json" {
		var cmd tea.Cmd
		m.Text, cmd = m.Text.Update(msg)
		return cmd
	}
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
func (m *Model) Value() string {
	if m.Field.Kind == "json" {
		return m.Text.Value()
	}
	return m.Input.Value()
}

func (m *Model) View() string {
	if m.Field.Kind == "json" {
		return m.Text.View() + "\nCtrl+S apply · Esc discard"
	}
	if m.Field.Kind == "select" && len(m.Field.Choices) > 0 {
		return m.Field.Label + ": ◀ " + m.Field.Choices[m.choice].Label + " ▶  Enter to select"
	}
	return m.Input.View()
}
