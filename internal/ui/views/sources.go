package views

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/form"
	"nixmox/internal/ui/components/section"
	"nixmox/internal/ui/components/table"
)

func (m *Model) prepareVersion(action string) {
	if action == "recipe-create" {
		m.openJSON("Create or import recipe", http.MethodPost, "/recipes", "recipe", map[string]any{
			"name": "",
			"sourceDefinition": map[string]any{
				"flakeDir": ".", "flakeConfiguration": "HOST",
				"fields": map[string]any{}, "observations": map[string]any{}, "outputs": map[string]any{},
			},
		})
		m.Form.Note += " Tracked YAML imports use recipeSource instead of sourceDefinition."
		return
	}
	if action == "configuration-create" {
		m.openJSON("Create configuration", http.MethodPost, "/configurations", "configuration", map[string]any{
			"name":       "",
			"recipe":     map[string]any{"id": "", "version": 1},
			"targetRepo": map[string]string{"id": "", "ref": "main", "sha": ""},
			"fields":     map[string]any{},
		})
		m.Form.Note += " For Git-only configurations, remove recipe and add flakeDir and flakeConfiguration."
		return
	}
	if m.Document == nil {
		return
	}
	document := m.Document
	path := m.versionPath()
	switch action {
	case "recipe-edit":
		m.openJSON("Edit recipe (tracked recipes fork)", http.MethodPut, "/recipes/"+url.PathEscape(document.ID), "recipe", map[string]any{
			"expectedVersion":  document.Version,
			"name":             document.Name,
			"sourceDefinition": document.Property("sourceDefinition"),
		})
	case "recipe-sync":
		m.queue(http.MethodPost, path+"/sync", "recipe", nil)
	case "configuration-inputs":
		var collected struct {
			Fields map[string]any `json:"fields"`
		}
		_ = json.Unmarshal(document.Property("collected"), &collected)
		m.openJSON("Set configuration inputs", http.MethodPut, "/configurations/"+url.PathEscape(document.ID), "configuration", map[string]any{
			"expectedVersion": document.Version, "fields": collected.Fields,
		})
	case "configuration-upgrade":
		m.openJSON("Upgrade recipe reference", http.MethodPost, path+"/upgrade", "configuration", map[string]any{"id": "", "version": 1})
	case "configuration-target":
		m.openJSON("Refresh explicit target pin", http.MethodPost, path+"/target", "configuration", document.Property("targetRepo"))
	case "configuration-observe":
		m.openJSON("Collect observations", http.MethodPost, path+"/observe", "configuration", map[string]string{"name": ""})
	case "configuration-render":
		m.queue(http.MethodPost, path+"/render", "preview", nil)
	case "show-version":
		kind, target := "recipes", "recipe"
		if m.Page == "Configurations" {
			kind, target = "configurations", "configuration"
		}
		m.Form = &form.Model{
			ID: "version", Title: "Read retained version",
			Fields: []form.Field{{ID: "version", Label: "Version", Value: strconv.FormatUint(document.Version, 10), Required: true}},
		}
		m.formRequest = &Request{Method: http.MethodGet, Path: "/" + kind + "/" + url.PathEscape(document.ID) + "/versions/", Target: target}
	}
}

func repositoriesPage(m *Model, v *View) {
	v.HeaderActions = []button.Model{m.action("Register repository", "repo-create", "")}
	documentPage(m, v)
	if m.Document != nil {
		v.Add(Block{Kind: Controls, Buttons: []button.Model{m.action("Refresh tracked ref", "repo-refresh", m.Document.ID)}})
	}
	m.formBlocks(v)
}

func recipesPage(m *Model, v *View) {
	v.HeaderActions = []button.Model{m.action("Create / import recipe", "recipe-create", "")}
	documentPage(m, v)
	if m.Document != nil {
		v.Add(Block{Kind: Controls, Buttons: []button.Model{
			m.action("Edit / fork", "recipe-edit", ""),
			m.versionAction("Sync", "recipe-sync", m.Document.Has("recipeSource"), "Detached recipes have no source to sync"),
			m.action("Read version", "show-version", ""),
		}})
	}
	m.formBlocks(v)
}

func configurationsPage(m *Model, v *View) {
	v.HeaderActions = []button.Model{m.action("Create configuration", "configuration-create", "")}
	documentPage(m, v)
	if m.Document != nil {
		recipeBacked := m.Document.Has("recipe")
		v.Add(Block{Kind: Controls, Buttons: []button.Model{
			m.versionAction("Set inputs", "configuration-inputs", recipeBacked, "Requires a recipe-backed configuration"),
			m.versionAction("Collect", "configuration-observe", recipeBacked, "Requires a recipe-backed configuration"),
			m.versionAction("Upgrade recipe", "configuration-upgrade", recipeBacked, "Requires a recipe-backed configuration"),
			m.action("Target pin", "configuration-target", ""),
			m.versionAction("Render preview", "configuration-render", string(m.Document.Property("canRender")) == "true", "Complete required inputs and observations first"),
			m.action("Read version", "show-version", ""),
			m.versionAction("Refresh Git configuration", "configuration-refresh", !recipeBacked, "Only Git-only configurations can refresh from the registry"),
			unavailable("Publish"), unavailable("Deploy / install"),
		}})
	}
	m.formBlocks(v)
	if len(m.Preview) > 0 {
		var preview struct {
			Contents map[string]string `json:"contents"`
		}
		if json.Unmarshal(m.Preview, &preview) == nil && len(preview.Contents) > 0 {
			for _, path := range sortedKeys(preview.Contents) {
				v.Add(Block{Kind: Sections, Section: section.Model{Title: clean(path)}}, Block{Kind: Preview, Lines: safeLines(preview.Contents[path])})
			}
		} else {
			v.Add(Block{Kind: Preview, Lines: pretty(m.Preview)})
		}
	}
}

func (m *Model) versionAction(label, kind string, available bool, reason string) button.Model {
	button := m.action(label, kind, "")
	if !available {
		button.Disabled, button.Hint = true, reason
	}
	return button
}

func documentPage(m *Model, v *View) {
	if m.Document != nil {
		v.Title = m.Document.Name
		v.Subtitle = "Version " + strconv.FormatUint(m.Document.Version, 10) + " · " + m.Document.ID
		if m.Page == "Repositories" {
			v.Subtitle = m.Document.ID
		}
		v.Add(Block{Kind: Controls, Buttons: []button.Model{control("Back", "back", "", false)}}, Block{Kind: Preview, Lines: pretty(m.Document.Raw)})
		return
	}
	records := table.Model{Title: m.Page, Columns: []string{"Name", "ID", "Version"}, Empty: "No records"}
	if m.Page == "Repositories" {
		records.Columns[2] = "Tracked ref"
	}
	for _, document := range m.documents() {
		if !matches(m.Query, document.Name, document.ID) {
			continue
		}
		version := strconv.FormatUint(document.Version, 10)
		if m.Page == "Repositories" {
			_ = json.Unmarshal(document.Property("ref"), &version)
		}
		records.Records = append(records.Records, table.Record{
			ID: document.ID, Action: Action{Kind: "document", ID: document.ID},
			Cells: []table.Cell{{Text: clean(document.Name)}, {Text: clean(document.ID)}, {Text: clean(version)}},
		})
	}
	v.Add(Block{Kind: Search}, Block{Kind: Tables, Table: records})
}
