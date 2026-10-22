package console

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"nixmox/internal/domain"
	"nixmox/internal/presentation"
)

// EditorModel is a reusable native preview/editor component, not a second service.
// Forms and file buffers survive async preview results and terminal resizes.
type EditorModel struct {
	State                presentation.Editor
	request              presentation.PreviewRequest
	inputs               []textinput.Model
	text                 textarea.Model
	focus, width, height int
	issues               []domain.FieldError
	confirmExit          bool
}

func NewEditor(request presentation.PreviewRequest) EditorModel {
	m := EditorModel{request: request, width: 80, height: 24, text: textarea.New()}
	m.State = presentation.Editor{Values: domain.Values{}, Files: append([]domain.DraftBuffer(nil), request.Files...), Revision: request.Revision}
	fields := (presentation.Shared{}).Form(request.Template, request.Values, nil).Fields
	for _, field := range fields {
		m.State.Values[field.Definition.Name] = field.Value
		input := textinput.New()
		input.Prompt = field.Definition.Name + ": "
		input.CharLimit = 65536
		value := field.Value
		if s, ok := value.(string); ok {
			input.SetValue(s)
		} else if value != nil {
			data, _ := json.Marshal(value)
			input.SetValue(string(data))
		}
		m.inputs = append(m.inputs, input)
	}
	if len(m.inputs) > 0 {
		m.inputs[0].Focus()
	} else {
		m.text.Focus()
	}
	m.resize()
	return m
}
func (m EditorModel) Init() tea.Cmd { return m.preview() }
func (m EditorModel) preview() tea.Cmd {
	request := m.request
	request.Revision = m.State.Revision
	request.Values = domain.Values{}
	for k, v := range m.State.Values {
		request.Values[k] = v
	}
	request.Files = append([]domain.DraftBuffer(nil), m.State.Files...)
	return func() tea.Msg { return (presentation.TemplateRenderer{}).Preview(request) }
}
func (m *EditorModel) resize() {
	m.text.SetWidth(max(20, m.width-4))
	m.text.SetHeight(max(3, m.height-len(m.inputs)-6))
	for i := range m.inputs {
		m.inputs[i].Width = max(10, m.width-len(m.inputs[i].Prompt)-4)
	}
}
func (m EditorModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case presentation.PreviewResult:
		if msg.Revision != m.State.Revision {
			return m, nil
		}
		m.issues = msg.Errors
		if len(msg.Files) == 0 {
			return m, nil
		}
		m.State.Apply(msg)
		if m.State.SelectedPath == "" {
			m.State.SelectedPath = msg.Files[0].Path
		}
		for _, file := range msg.Files {
			if file.Path == m.State.SelectedPath && m.text.Value() != file.Content && !file.ManuallyEdited {
				m.text.SetValue(file.Content)
			}
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.State.Dirty && !m.confirmExit {
				m.confirmExit = true
				return m, nil
			}
			return m, tea.Quit
		case "tab", "shift+tab":
			delta := 1
			if msg.String() == "shift+tab" {
				delta = -1
			}
			m.focus = (m.focus + delta + len(m.inputs) + 1) % (len(m.inputs) + 1)
			for i := range m.inputs {
				m.inputs[i].Blur()
			}
			m.text.Blur()
			if m.focus == len(m.inputs) {
				return m, m.text.Focus()
			}
			return m, m.inputs[m.focus].Focus()
		}
	}
	var command tea.Cmd
	if m.focus == len(m.inputs) {
		previous := m.text.Value()
		m.text, command = m.text.Update(message)
		if m.text.Value() != previous && m.State.SelectedPath != "" {
			_ = m.State.Edit(m.State.SelectedPath, m.text.Value())
		}
	} else {
		previous := m.inputs[m.focus].Value()
		m.inputs[m.focus], command = m.inputs[m.focus].Update(message)
		input := m.inputs[m.focus].Value()
		if input != previous {
			definition := m.request.Template.Fields[m.focus]
			var value any = input
			switch definition.Type {
			case "integer":
				value = json.Number(input)
			case "boolean":
				parsed, err := strconv.ParseBool(input)
				if err == nil {
					value = parsed
				}
			case "multiselect":
				var list []string
				if json.Unmarshal([]byte(input), &list) == nil {
					value = list
				}
			}
			m.State.Values[definition.Name] = value
			m.State.Revision++
			m.State.Dirty = true
			m.confirmExit = false
			return m, tea.Batch(command, m.preview())
		}
	}
	return m, command
}
func (m EditorModel) View() string {
	var view strings.Builder
	view.WriteString("Configuration preview · Tab/Shift-Tab focus · Esc exit\n")
	for _, input := range m.inputs {
		view.WriteString(input.View() + "\n")
	}
	view.WriteString(m.State.SelectedPath + "\n" + m.text.View() + "\n")
	for _, file := range m.State.Files {
		if file.Regenerated != nil {
			fmt.Fprintf(&view, "%s: manual edits retained; regenerated output needs review\n", file.Path)
		}
	}
	for _, issue := range m.issues {
		fmt.Fprintf(&view, "%s: %s\n", issue.Field, issue.Message)
	}
	if m.State.Dirty {
		view.WriteString("Unsaved edits\n")
	}
	if m.confirmExit {
		view.WriteString("Unsaved edits will be lost. Press Esc again to discard.\n")
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(view.String())
}
