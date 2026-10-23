package terminal

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	logs "nixmox/internal/ui/components/log"
	"nixmox/internal/ui/components/table"
	"nixmox/internal/ui/components/telemetry"
	"nixmox/internal/ui/views"
)

func TestNewSharedPageAndListNeedNoTerminalRegistration(t *testing.T) {
	original := views.Pages
	defer func() { views.Pages = original }()
	views.Pages = append(append([]views.Page(nil), views.Pages...), views.Page{Name: "Example", Build: func(_ *views.Model, v *views.View) {
		v.Add(
			views.Block{Kind: views.Metrics, Metrics: []telemetry.Model{{Label: "Shared graph", Known: true, History: []float64{.2, .5, .3}}}},
			views.Block{Kind: views.Tables, Table: table.Model{Title: "Another list", Columns: []string{"Name"}, Records: []table.Record{{ID: "row", Marker: "⠋", Tone: "running", Action: views.Action{Kind: "page", ID: "Hosts"}, Cells: []table.Cell{{Text: "Shared row"}}}}}},
			views.Block{Kind: views.Logs, Log: &logs.Model{Lines: []string{"Shared log"}, Total: 1}},
		)
	}})
	m := New(views.New())
	m.Update(tea.KeyPressMsg{Code: '5', Text: "5"})
	text := ansi.Strip(m.View().Content)
	for _, label := range []string{"Example", "ANOTHER LIST", "Shared row", "Shared graph", "Shared log", "⠋"} {
		if !strings.Contains(text, label) {
			t.Fatal("generic renderer did not render shared page", label)
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.focus != (views.Action{Kind: "page", ID: "Hosts"}) {
		t.Fatal("list selection is hardcoded to a feature")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Core.Page != "Hosts" {
		t.Fatal("generic list action not activated")
	}
}
