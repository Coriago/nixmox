package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"nixmox/internal/client"
	"nixmox/internal/configuration"
	"nixmox/internal/domain"
	"nixmox/internal/render"
)

type sourceMessage struct {
	mode      string
	repos     []domain.Repository
	repo      domain.Repository
	templates configuration.TemplateSet
	bundle    configuration.TemplateBundle
	draft     *configuration.Draft
	err       error
}
type sourceModel struct {
	client        *client.Client
	ctx           context.Context
	width, height int
	mode, notice  string
	busy          bool
	repos         []domain.Repository
	repo          domain.Repository
	selection     int
	templates     configuration.TemplateSet
	bundle        configuration.TemplateBundle
	inputs        []textinput.Model
	field         int
	editor        textarea.Model
	editorFocus   bool
	files         []render.File
	file          int
	values        map[string]any
	edited        map[string]bool
	regenerated   map[string]string
	resolved      map[string]bool
	draft         *configuration.Draft
}

func newSources(ctx context.Context, c *client.Client, width, height int) *sourceModel {
	editor := textarea.New()
	editor.SetWidth(max(30, width/2-8))
	editor.SetHeight(max(8, height-14))
	editor.CharLimit = domain.MaxCommandBytes
	return &sourceModel{ctx: ctx, client: c, width: width, height: height, mode: "repositories", notice: "Loading repositories…", busy: true, editor: editor, edited: map[string]bool{}, regenerated: map[string]string{}, resolved: map[string]bool{}}
}
func (m *sourceModel) fetch() tea.Cmd {
	return func() tea.Msg {
		var page domain.Page[domain.Repository]
		err := m.client.Read(m.ctx, "/api/v1/repositories?limit=500", &page)
		return sourceMessage{mode: "repositories", repos: page.Items, err: err}
	}
}
func (m *sourceModel) openRepository() tea.Cmd {
	id := m.repos[m.selection].ID
	return func() tea.Msg {
		var r domain.Repository
		err := m.client.Read(m.ctx, "/api/v1/repositories/"+id, &r)
		return sourceMessage{mode: "repository", repo: r, err: err}
	}
}
func (m *sourceModel) templatesCommand() tea.Cmd {
	id := m.repo.ID
	return func() tea.Msg {
		var set configuration.TemplateSet
		err := m.client.Read(m.ctx, "/api/v1/repositories/"+id+"/templates", &set)
		return sourceMessage{mode: "templates", templates: set, err: err}
	}
}
func (m *sourceModel) templateCommand() tea.Cmd {
	id, pin, templateID := m.repo.ID, m.templates.Commit, m.templates.Items[m.selection].ID
	return func() tea.Msg {
		var b configuration.TemplateBundle
		err := m.client.Read(m.ctx, "/api/v1/repositories/"+id+"/templates/"+url.PathEscape(templateID)+"?commit="+pin, &b)
		return sourceMessage{mode: "editor", bundle: b, err: err}
	}
}
func (m *sourceModel) setupInputs() {
	m.inputs = nil
	m.field = 0
	m.editorFocus = false
	m.editor.Blur()
	for _, f := range m.bundle.Template.Fields {
		input := textinput.New()
		input.Prompt = f.Name + " > "
		input.Width = 30
		input.CharLimit = 4096
		v, ok := m.values[f.Name]
		if !ok {
			v = f.Default
		}
		if v != nil {
			if s, ok := v.(string); ok {
				input.SetValue(s)
			} else {
				b, _ := json.Marshal(v)
				input.SetValue(string(b))
			}
		}
		m.inputs = append(m.inputs, input)
	}
	if len(m.inputs) > 0 {
		m.inputs[0].Focus()
	}
}
func (m *sourceModel) inputValues() map[string]any {
	values := map[string]any{}
	for i, f := range m.bundle.Template.Fields {
		text := m.inputs[i].Value()
		var value any = text
		switch f.Type {
		case "boolean", "integer", "multiselect":
			if json.Unmarshal([]byte(text), &value) != nil {
				value = text
			}
		}
		values[f.Name] = value
	}
	return values
}
func (m *sourceModel) preview() {
	m.values = m.inputValues()
	result, err := render.Render(m.bundle.Template, m.values, m.bundle.Templates)
	if err != nil {
		m.notice = err.Error()
		return
	}
	next := map[string]render.File{}
	for _, f := range result.Files {
		next[f.Path] = f
	}
	for _, old := range m.files {
		if m.edited[old.Path] {
			fresh, ok := next[old.Path]
			if !ok || fresh.Content != old.Content {
				m.regenerated[old.Path] = fresh.Content
			}
			next[old.Path] = old
		}
	}
	m.files = nil
	for _, f := range next {
		m.files = append(m.files, f)
	}
	sort.Slice(m.files, func(i, j int) bool { return m.files[i].Path < m.files[j].Path })
	m.file = min(m.file, max(0, len(m.files)-1))
	m.showFile()
	m.notice = "Unsaved preview · " + m.bundle.Commit
}
func (m *sourceModel) showFile() {
	if len(m.files) > 0 && m.editor.Value() != m.files[m.file].Content {
		m.editor.SetValue(m.files[m.file].Content)
	}
}
func sourceSubmit(ctx context.Context, c *client.Client, target, kind string, p any, versions map[string]int64) (domain.Task, error) {
	b, _ := json.Marshal(p)
	cmd := domain.Command{ID: domain.NewID("cmd_"), Type: kind, TargetID: target, Payload: b, ExpectedVersions: versions}
	receipt, err := c.Submit(ctx, cmd)
	if err != nil {
		return domain.Task{}, fmt.Errorf("%s · command %s", err, cmd.ID)
	}
	task, err := c.Wait(ctx, receipt.TaskID)
	if err != nil {
		return task, err
	}
	if task.State != "succeeded" {
		if task.Error != nil {
			return task, task.Error
		}
		return task, domain.Err("TASK_INCOMPLETE", "Task requires attention: "+task.ID, 409)
	}
	return task, nil
}
func (m *sourceModel) saveCommand(action string) tea.Cmd {
	values := m.inputValues()
	files := append([]render.File(nil), m.files...)
	manual, resolved := map[string]bool{}, map[string]bool{}
	for k, v := range m.edited {
		manual[k] = v
	}
	for k, v := range m.resolved {
		resolved[k] = v
	}
	var draft *configuration.Draft
	if m.draft != nil {
		copy := *m.draft
		draft = &copy
	}
	repo, bundle, ctx, c := m.repo, m.bundle, m.ctx, m.client
	return func() tea.Msg {
		fail := func(err error) tea.Msg { return sourceMessage{err: err} }
		identity, err := c.Identity(ctx)
		if err != nil {
			return fail(err)
		}
		if draft == nil {
			task, err := sourceSubmit(ctx, c, identity.NodeID, "configuration.generate", map[string]any{"repository_id": repo.ID, "template_id": bundle.Template.ID, "base_commit": bundle.Commit, "values": values}, nil)
			if err != nil {
				return fail(err)
			}
			var result struct {
				DraftID string `json:"draft_id"`
			}
			if json.Unmarshal(task.Result, &result) != nil {
				return fail(fmt.Errorf("invalid draft result"))
			}
			draft = &configuration.Draft{}
			if err = c.Read(ctx, "/api/v1/drafts/"+result.DraftID, draft); err != nil {
				return fail(err)
			}
		}
		read := func() error { return c.Read(ctx, "/api/v1/drafts/"+draft.ID, draft) }
		editFile := func(f render.File) error {
			_, err := sourceSubmit(ctx, c, draft.OwnerNodeID, "configuration.draft.edit", map[string]any{"draft_id": draft.ID, "file": map[string]string{"path": f.Path, "content": f.Content}}, map[string]int64{"draft:" + draft.ID: draft.Version})
			if err != nil {
				return err
			}
			return read()
		}
		for _, f := range files {
			if !manual[f.Path] {
				continue
			}
			unchanged := false
			for _, saved := range draft.Files {
				if saved.Path == f.Path && saved.Content == f.Content {
					unchanged = true
				}
			}
			if unchanged && !resolved[f.Path] {
				continue
			}
			if err = editFile(f); err != nil {
				return fail(err)
			}
		}
		before, _ := json.Marshal(draft.Values)
		after, _ := json.Marshal(values)
		if string(before) != string(after) {
			_, err = sourceSubmit(ctx, c, draft.OwnerNodeID, "configuration.draft.edit", map[string]any{"draft_id": draft.ID, "values": values}, map[string]int64{"draft:" + draft.ID: draft.Version})
			if err != nil {
				return fail(err)
			}
			if err = read(); err != nil {
				return fail(err)
			}
			for _, f := range files {
				if resolved[f.Path] {
					if err = editFile(f); err != nil {
						return fail(err)
					}
				}
			}
		}
		if action != "save" {
			kind := "configuration.validate"
			var versions map[string]int64
			if action == "commit" {
				kind = "configuration.commit"
				versions = draft.ExpectedVersions
			}
			_, err = sourceSubmit(ctx, c, draft.OwnerNodeID, kind, map[string]any{"draft_id": draft.ID}, versions)
			if err != nil {
				return fail(err)
			}
			if err = read(); err != nil {
				return fail(err)
			}
		}
		return sourceMessage{mode: "editor", draft: draft, bundle: bundle}
	}
}
func (m *sourceModel) update(msg tea.Msg) tea.Cmd {
	switch v := msg.(type) {
	case sourceMessage:
		m.busy = false
		if v.err != nil {
			m.notice = v.err.Error()
			return nil
		}
		m.mode = v.mode
		m.selection = 0
		m.notice = ""
		switch v.mode {
		case "repositories":
			m.repos = v.repos
		case "repository":
			m.repo = v.repo
		case "templates":
			m.templates = v.templates
			m.templates.Items = nil
			for _, t := range v.templates.Items {
				if t.Kind == "configuration" {
					m.templates.Items = append(m.templates.Items, t)
				}
			}
			if len(m.templates.Items) == 1 {
				m.busy = true
				return m.templateCommand()
			}
			if len(m.templates.Items) == 0 {
				m.notice = "No configuration templates at this revision."
			}
		case "editor":
			m.bundle = v.bundle
			m.draft = v.draft
			m.values = map[string]any{}
			m.files = nil
			m.edited = map[string]bool{}
			m.regenerated = map[string]string{}
			m.resolved = map[string]bool{}
			if v.draft != nil {
				m.values = v.draft.Values
				m.files = v.draft.Files
				m.edited = v.draft.ManuallyEdited
				m.regenerated = v.draft.Regenerated
				m.notice = fmt.Sprintf("%s · version %d · %s", v.draft.ID, v.draft.Version, v.draft.Validation.State)
			}
			m.setupInputs()
			if v.draft == nil {
				m.preview()
			} else {
				m.showFile()
			}
		}
		return nil
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.editor.SetWidth(max(24, v.Width/2-8))
		if v.Width < 100 {
			m.editor.SetWidth(max(24, v.Width-8))
		}
		m.editor.SetHeight(max(8, v.Height-14))
	case tea.KeyMsg:
		if m.busy {
			return nil
		}
		if m.mode == "editor" {
			switch v.String() {
			case "ctrl+s":
				m.busy = true
				return m.saveCommand("save")
			case "ctrl+v":
				m.busy = true
				return m.saveCommand("validate")
			case "ctrl+k":
				if len(m.regenerated) > 0 {
					m.notice = "Review regenerated text with F4/F5 before committing."
					return nil
				}
				m.busy = true
				return m.saveCommand("commit")
			case "tab":
				m.editorFocus = !m.editorFocus
				for i := range m.inputs {
					m.inputs[i].Blur()
				}
				if m.editorFocus {
					return m.editor.Focus()
				}
				m.editor.Blur()
				if len(m.inputs) > 0 {
					return m.inputs[m.field].Focus()
				}
				return nil
			case "f2":
				if len(m.inputs) > 0 {
					m.inputs[m.field].Blur()
					m.field = (m.field + 1) % len(m.inputs)
					m.editorFocus = false
					m.editor.Blur()
					return m.inputs[m.field].Focus()
				}
				return nil
			case "f3":
				if len(m.files) > 0 {
					m.file = (m.file + 1) % len(m.files)
					m.showFile()
				}
				return nil
			case "f4", "f5":
				if len(m.files) > 0 {
					f := &m.files[m.file]
					if text, ok := m.regenerated[f.Path]; ok {
						if v.String() == "f4" {
							f.Content = text
							m.showFile()
						}
						m.edited[f.Path] = true
						m.resolved[f.Path] = true
						delete(m.regenerated, f.Path)
					}
				}
				return nil
			}
			var cmd tea.Cmd
			if m.editorFocus {
				m.editor, cmd = m.editor.Update(msg)
				if len(m.files) > 0 && m.editor.Value() != m.files[m.file].Content {
					m.files[m.file].Content = m.editor.Value()
					m.edited[m.files[m.file].Path] = true
					m.notice = "Unsaved file edits"
				}
			} else if len(m.inputs) > 0 {
				old := m.inputs[m.field].Value()
				m.inputs[m.field], cmd = m.inputs[m.field].Update(msg)
				if old != m.inputs[m.field].Value() {
					m.preview()
				}
			}
			return cmd
		}
		if m.mode == "import" || m.mode == "open" {
			switch v.String() {
			case "tab":
				m.inputs[m.field].Blur()
				m.field = (m.field + 1) % len(m.inputs)
				return m.inputs[m.field].Focus()
			case "enter":
				fields := []string{}
				for _, input := range m.inputs {
					fields = append(fields, input.Value())
				}
				mode, ctx, c := m.mode, m.ctx, m.client
				m.busy = true
				return func() tea.Msg {
					if mode == "open" {
						var d configuration.Draft
						err := c.Read(ctx, "/api/v1/drafts/"+url.PathEscape(fields[0]), &d)
						if err != nil {
							return sourceMessage{err: err}
						}
						var b configuration.TemplateBundle
						err = c.Read(ctx, "/api/v1/repositories/"+d.RepositoryID+"/templates/"+d.TemplateID+"?commit="+d.BaseCommit, &b)
						return sourceMessage{mode: "editor", draft: &d, bundle: b, err: err}
					}
					identity, err := c.Identity(ctx)
					if err == nil {
						_, err = sourceSubmit(ctx, c, identity.NodeID, "repository.import", map[string]string{"name": fields[0], "remote_url": fields[1], "upstream_branch": fields[2]}, nil)
					}
					var page domain.Page[domain.Repository]
					if err == nil {
						err = c.Read(ctx, "/api/v1/repositories", &page)
					}
					return sourceMessage{mode: "repositories", repos: page.Items, err: err}
				}
			}
			var cmd tea.Cmd
			m.inputs[m.field], cmd = m.inputs[m.field].Update(msg)
			return cmd
		}
		switch v.String() {
		case "b":
			m.busy = true
			return m.fetch()
		case "up", "k":
			m.selection = max(0, m.selection-1)
		case "down", "j":
			count := len(m.repos)
			if m.mode == "templates" {
				count = len(m.templates.Items)
			}
			m.selection = min(max(0, count-1), m.selection+1)
		case "enter":
			if m.mode == "repositories" && len(m.repos) > 0 {
				m.busy = true
				return m.openRepository()
			}
			if m.mode == "templates" && len(m.templates.Items) > 0 {
				m.busy = true
				return m.templateCommand()
			}
		case "n":
			if m.mode == "repository" {
				m.busy = true
				return m.templatesCommand()
			}
		case "i", "o":
			m.mode = "import"
			labels := []string{"Name", "Remote URL", "Upstream branch"}
			if v.String() == "o" {
				m.mode = "open"
				labels = []string{"Draft ID"}
			}
			m.inputs = nil
			m.field = 0
			for _, label := range labels {
				input := textinput.New()
				input.Prompt = label + " > "
				input.Width = 60
				input.CharLimit = 4096
				if label == "Upstream branch" {
					input.SetValue("main")
				}
				m.inputs = append(m.inputs, input)
			}
			return m.inputs[0].Focus()
		case "f":
			if m.mode == "repository" {
				id, ctx, c := m.repo.ID, m.ctx, m.client
				m.busy = true
				return func() tea.Msg {
					identity, err := c.Identity(ctx)
					if err == nil {
						_, err = sourceSubmit(ctx, c, identity.NodeID, "repository.fetch", map[string]string{"repository_id": id}, nil)
					}
					var r domain.Repository
					if err == nil {
						err = c.Read(ctx, "/api/v1/repositories/"+id, &r)
					}
					return sourceMessage{mode: "repository", repo: r, err: err}
				}
			}
		}
	}
	return nil
}
func (m *sourceModel) view() string {
	var b strings.Builder
	b.WriteString("Nixmox · Sources\n\n")
	switch m.mode {
	case "repositories":
		for i, r := range m.repos {
			mark := "  "
			if i == m.selection {
				mark = "> "
			}
			fmt.Fprintf(&b, "%s%s · %s\n", mark, r.Name, r.Remote.URL)
		}
		b.WriteString("\nEnter open · i import · o open draft")
	case "repository":
		fmt.Fprintf(&b, "%s · %s\n", m.repo.Name, m.repo.Remote.URL)
		for _, r := range m.repo.Refs {
			fmt.Fprintf(&b, "%s · %s · +%d/-%d\n", r.Name, r.Head, r.Ahead, r.Behind)
		}
		for _, c := range m.repo.Configurations {
			fmt.Fprintf(&b, "%s · %s\n", c.Name, c.Readiness)
		}
		b.WriteString("\nn new configuration · f fetch · o open draft")
	case "templates":
		for i, t := range m.templates.Items {
			mark := "  "
			if i == m.selection {
				mark = "> "
			}
			fmt.Fprintf(&b, "%s%s (%s)\n", mark, t.Label, t.ID)
		}
		b.WriteString("\nEnter select pinned template")
	case "import", "open":
		for _, input := range m.inputs {
			b.WriteString(input.View() + "\n")
		}
		b.WriteString("\nTab next field · Enter submit")
	case "editor":
		forms := []string{}
		for i, input := range m.inputs {
			forms = append(forms, input.View())
			if len(m.bundle.Template.Fields[i].Choices) > 0 {
				forms = append(forms, "  choices: "+strings.Join(m.bundle.Template.Fields[i].Choices, ", "))
			}
		}
		editor := ""
		if len(m.files) > 0 {
			f := m.files[m.file]
			editor = f.Path + "\n" + m.editor.View()
			if text, ok := m.regenerated[f.Path]; ok {
				editor += "\nRegenerated text (F4 replace / F5 keep edits):\n" + text
			}
		}
		if m.width >= 100 {
			b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(m.width/2).Render(strings.Join(forms, "\n")), editor))
		} else if m.editorFocus {
			b.WriteString(editor)
		} else {
			b.WriteString(strings.Join(forms, "\n"))
		}
		b.WriteString("\nTab form/editor · F2 next field · F3 next file\nCtrl+S save · Ctrl+V validate · Ctrl+K commit")
	}
	b.WriteString("\n\n" + m.notice)
	if m.busy {
		b.WriteString("\nWorking…")
	}
	b.WriteString("\nb repository list · Esc back (preserves editor) · Ctrl+C exit")
	return b.String()
}
