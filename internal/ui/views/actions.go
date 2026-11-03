package views

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/confirmation"
)

func (m *Model) action(label, kind, id string) button.Model {
	control := control(label, kind, id, false)
	if !m.connected {
		control.Disabled = true
		control.Hint = "Backend is not connected"
	}
	if m.busy {
		control.Disabled = true
		control.Hint = "A request is in progress"
	}
	return control
}
func unavailable(label string) button.Model {
	return button.Model{Label: label, Disabled: true, Hint: "Not implemented by this backend"}
}
func (m *Model) queue(method, path, target string, body any) {
	var data json.RawMessage
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			m.Notice = err.Error()
			return
		}
		data = encoded
	}
	m.pending = &Request{Method: method, Path: path, Target: target, Body: data}
	m.busy = true
}
func (m *Model) prepare(action Action) {
	if !m.connected || m.busy {
		return
	}
	switch action.Kind {
	case "refresh":
		m.queue("", "", "refresh", nil)
	case "cluster-create":
		m.openClusterForm()
	case "repo-create":
		m.openJSON("Register repository", http.MethodPost, "/repos", "repository", map[string]any{"name": "", "url": "", "ref": "main"})
	case "repo-refresh":
		m.queue(http.MethodPost, "/repos/"+url.PathEscape(action.ID)+"/refresh", "repository", nil)
	case "recipe-create", "recipe-edit", "recipe-sync", "configuration-create", "configuration-inputs", "configuration-upgrade", "configuration-target", "configuration-observe", "configuration-render", "show-version":
		m.prepareVersion(action.Kind)
	case "configuration-refresh":
		if m.Document != nil {
			m.queue(http.MethodPost, "/configurations/"+url.PathEscape(m.Document.ID)+"/refresh", "configuration", nil)
		}
	case "build":
		m.openBuildForm(action.ID)
	case "cancel":
		m.review = &Request{Method: http.MethodPost, Path: "/tasks/" + url.PathEscape(action.ID) + "/cancel"}
		m.Modal = &confirmation.Model{Title: "Cancel task?", Body: "The owning host will stop this task. Completed external effects are not rolled back.", Confirm: "Cancel task"}
	case "retry":
		m.queue(http.MethodPost, "/tasks/"+url.PathEscape(action.ID)+"/retry", "task", nil)
	case "approve":
		task := m.Task(action.ID)
		if task != nil && task.Source.Review != nil {
			m.queue(http.MethodPost, "/tasks/"+url.PathEscape(action.ID)+"/approve", "", map[string]string{"outputId": task.Source.Review.OutputID})
		}
	case "task-output":
		m.queue(http.MethodGet, "/tasks/"+url.PathEscape(m.TaskID)+"/outputs/"+url.PathEscape(action.ID), "output", nil)
	}
}
func (m *Model) documents() []Document {
	switch m.Page {
	case "Recipes":
		return m.Recipes
	case "Configurations":
		return m.Configurations
	case "Repositories":
		return m.Repositories
	}
	return nil
}
func (m *Model) selectDocument(id string) {
	for _, document := range m.documents() {
		if document.ID == id {
			m.Document = &document
			m.DocumentID = id
			m.Preview = nil
			return
		}
	}
}
func (m *Model) versionPath() string {
	if m.Document == nil {
		return ""
	}
	kind := "recipes"
	if m.Page == "Configurations" {
		kind = "configurations"
	}
	return fmt.Sprintf("/%s/%s/versions/%d", kind, url.PathEscape(m.Document.ID), m.Document.Version)
}
