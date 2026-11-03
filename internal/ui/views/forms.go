package views

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/form"
)

func (m *Model) openJSON(title, method, path, target string, value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		m.Notice = err.Error()
		return
	}
	m.Form = &form.Model{
		ID: title, Title: title,
		Note:   "Edit JSON inputs. Requests go to the daemon; this view never changes stored records locally.",
		Fields: []form.Field{{ID: "json", Label: "Inputs", Kind: "json", Value: string(data), Required: true}},
	}
	m.formRequest = &Request{Method: method, Path: path, Target: target}
}
func (m *Model) openClusterForm() {
	m.Form = &form.Model{ID: "cluster", Title: "Create cluster", Fields: []form.Field{{ID: "name", Label: "Cluster name", Required: true}}}
	m.formRequest = &Request{Method: http.MethodPost, Path: "/cluster"}
}
func (m *Model) openBuildForm(hostID string) {
	if hostID != m.Health.NodeID {
		m.Notice = "Remote build dispatch is not implemented"
		return
	}
	m.openJSON("Build pinned Nix installable", http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/builds", "task", map[string]any{
		"targetRepo":  map[string]string{"id": "", "ref": "main", "sha": ""},
		"flakeDir":    ".",
		"installable": "configurations.nixos.HOST.config.system.build.toplevel",
	})
}
func (m *Model) SetFormValue(id, field, value string) {
	if m.busy || m.Form == nil || m.Form.ID != id || len(value) > form.InputLimit {
		return
	}
	for i := range m.Form.Fields {
		if m.Form.Fields[i].ID == field {
			m.Form.Fields[i].Value = value
		}
	}
}

// Existing form adapters expose secret input, but no credential mutation is wired
// until the backend implements it. Never retain a secret in presentation state.
func (m *Model) SetFormSecret(string, []byte) {}
func (m *Model) submitForm() {
	if m.Form == nil || m.formRequest == nil || m.busy {
		return
	}
	request := *m.formRequest
	for _, field := range m.Form.Fields {
		if field.Required && field.Value == "" {
			m.Notice = field.Label + " is required"
			return
		}
		switch field.ID {
		case "json":
			if !json.Valid([]byte(field.Value)) {
				m.Notice = "Inputs must contain one valid JSON value"
				return
			}
			request.Body = json.RawMessage(field.Value)
		case "name":
			request.Body, _ = json.Marshal(map[string]string{"name": field.Value})
		case "version":
			version, err := strconv.ParseUint(field.Value, 10, 64)
			if err != nil || version == 0 {
				m.Notice = "Version must be a positive number"
				return
			}
			request.Path += strconv.FormatUint(version, 10)
		}
	}
	m.pending = &request
	m.busy = true
}
func (m *Model) formBlocks(v *View) {
	if m.Form == nil {
		return
	}
	form := *m.Form
	form.Disabled = m.busy
	v.Add(Block{Kind: Forms, Form: &form}, Block{Kind: Controls, Buttons: []button.Model{
		m.action("Submit", "source-form-save", ""),
		control("Close", "source-form-close", "", false),
	}})
}
func pretty(data json.RawMessage) []string {
	var output bytes.Buffer
	if err := json.Indent(&output, data, "", "  "); err != nil {
		return []string{"No structured output available"}
	}
	return safeLines(output.String())
}
