// Package views owns presentation state shared by the browser and terminal.
// HTTP lifetimes belong to Connection; durable state always comes from the API.
package views

import (
	"encoding/json"
	"slices"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/ui"
	"nixmox/internal/ui/components/confirmation"
	"nixmox/internal/ui/components/form"
	logs "nixmox/internal/ui/components/log"
	"nixmox/internal/ui/components/theme"
)

type Action = ui.Action

type Model struct {
	Page, HostID, TaskID, DocumentID, Tab, Filter, Query string
	ThemeIndex, AccentIndex                              int
	Reduced                                              bool
	Modal                                                *confirmation.Model
	Form                                                 *form.Model
	Health                                               domain.Health
	Cluster                                              domain.Cluster
	Hosts                                                []Host
	Tasks                                                []Task
	Repositories, Recipes, Configurations                []Document
	Document                                             *Document
	Preview                                              json.RawMessage
	Notice                                               string
	LogFollow                                            logs.FollowState
	HostLog                                              logs.Model
	lastHostLog                                          map[string]uint64
	formRequest                                          *Request
	pending, review                                      *Request
	busy, connected                                      bool
	connectionError, warnings                            string
	frame                                                uint64
}

func New() *Model {
	return &Model{Page: "Cluster", Tab: "Overview", Filter: "All", LogFollow: logs.FollowState{Follow: true}, lastHostLog: map[string]uint64{}}
}
func (m *Model) Close() { m.Form = nil; m.pending = nil; m.review = nil }
func (m *Model) Theme() theme.Theme {
	palette := theme.Themes[m.ThemeIndex]
	if m.AccentIndex > 0 {
		palette.Accent = []string{"", "#f18a24", "#d96a00", "#efa345"}[m.AccentIndex]
	}
	return palette
}
func (m *Model) Host(id string) *Host {
	return ui.Find(m.Hosts, func(h Host) bool { return h.ID == id })
}
func (m *Model) Task(id string) *Task {
	return ui.Find(m.Tasks, func(t Task) bool { return t.ID == id })
}
func (m *Model) Route() [4]string {
	return [4]string{m.Page, m.HostID, m.TaskID, m.DocumentID + "/" + m.Tab}
}
func (m *Model) Selection() Selection { return Selection{Host: m.HostID, Task: m.TaskID} }
func (m *Model) Advance(dt time.Duration) {
	if !m.Reduced && dt > 0 {
		m.frame++
	}
}
func (m *Model) TakeCommand() *Request {
	request := m.pending
	m.pending = nil
	return request
}

func (m *Model) Dispatch(action Action) {
	if m.Modal != nil {
		switch action.Kind {
		case "dismiss":
			m.Modal = nil
			m.review = nil
		case "confirm":
			m.pending = m.review
			m.review = nil
			m.Modal = nil
			m.busy = m.pending != nil
		}
		return
	}
	switch action.Kind {
	case "page":
		if !slices.ContainsFunc(Pages, func(p Page) bool { return p.Name == action.ID }) {
			return
		}
		m.Page = action.ID
		m.HostID = ""
		m.TaskID = ""
		m.DocumentID = ""
		m.Document = nil
		m.Preview = nil
		m.Query = ""
		m.Form = nil
	case "host":
		m.Page = "Hosts"
		m.HostID = action.ID
		m.TaskID = ""
		m.HostLog = logs.Model{}
		delete(m.lastHostLog, "hosts/"+action.ID)
	case "task":
		m.Preview = nil
		m.Page = "Tasks"
		m.TaskID = action.ID
		m.HostID = ""
		m.Tab = "Overview"
	case "document":
		m.selectDocument(action.ID)
	case "back":
		m.HostID = ""
		m.TaskID = ""
		m.DocumentID = ""
		m.Document = nil
		m.Form = nil
		m.Preview = nil
	case "tab":
		m.Tab = action.ID
	case "follow":
		m.LogFollow.Follow = true
	case "search":
		m.Query = ui.Text(action.ID, 128, -1)
	case "filter":
		m.Filter = action.ID
	case "theme":
		m.ThemeIndex = (m.ThemeIndex + 1) % len(theme.Themes)
	case "accent":
		m.AccentIndex = (m.AccentIndex + 1) % 4
	case "motion":
		m.Reduced = !m.Reduced
	case "help":
		m.Modal = &confirmation.Model{Title: "Keyboard and mouse", Body: "Tab / Shift-Tab focus · Enter activate · Esc back · 1–9 pages · / filter · t theme · m motion · q quit TUI. JSON forms use Ctrl+S to apply multiline terminal input.", Confirm: "Got it"}
	case "source-form-close":
		m.Form = nil
		m.formRequest = nil
	case "source-form-save":
		m.submitForm()
	default:
		m.prepare(action)
	}
	m.LogFollow.Follow = true
}
func Shortcut(key string) (Action, bool) {
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		index := int(key[0] - '1')
		if index < len(Pages) {
			return Action{Kind: "page", ID: Pages[index].Name}, true
		}
	}
	kind, ok := map[string]string{"/": "search-focus", "t": "theme", "m": "motion", "?": "help", "Escape": "back", "esc": "back"}[key]
	return Action{Kind: kind}, ok
}
