package views

import (
	"reflect"
	"testing"

	logs "nixmox/internal/ui/components/log"
	"nixmox/internal/ui/components/status"
	"nixmox/internal/ui/components/table"
	"nixmox/internal/ui/components/telemetry"
)

func TestNewPageUsesExistingLayoutAndComponents(t *testing.T) {
	original := Pages
	defer func() { Pages = original }()
	Pages = append(append([]Page(nil), Pages...), Page{Name: "Example", Build: func(_ *Model, v *View) {
		marker, tone := status.Marker("running", 0, false)
		v.Add(
			Block{Kind: Tables, Table: table.Model{Title: "Another list", Columns: []string{"Name"}, Records: []table.Record{{ID: "example", Marker: marker, Tone: tone, Action: Action{Kind: "page", ID: "Hosts"}, Cells: []table.Cell{{Text: "Shared row"}}}}}},
			Block{Kind: Metrics, Metrics: []telemetry.Model{{Label: "Shared graph", Known: true, History: []float64{.1, .3, .2}}}},
			Block{Kind: Logs, Log: &logs.Model{Lines: []string{"Shared log"}, Total: 1}},
		)
	}})
	m := New()
	a, ok := Shortcut("5")
	if !ok || a.ID != "Example" {
		t.Fatal("page shortcut needs renderer-specific registration")
	}
	m.Dispatch(a)
	v := m.View()
	if v.Title != "Example" || len(v.Nav) != 5 || v.Find(Tables).Table.Title != "Another list" {
		t.Fatal("shared page not registered")
	}
	var kinds []BlockKind
	for _, b := range v.Blocks {
		kinds = append(kinds, b.Kind)
	}
	if !reflect.DeepEqual(kinds, []BlockKind{Heading, Tables, Metrics, Logs}) {
		t.Fatal("page composition not defined by shared layout", kinds)
	}
	if v.Find(Metrics).Metrics[0].Visualization() != telemetry.History || v.Find(Tables).Table.Records[0].Marker == "" {
		t.Fatal("shared spinner/graph definition missing")
	}
}
func TestAllPagesUseSharedBlockOrdering(t *testing.T) {
	m := New()
	for _, p := range Pages {
		m.Dispatch(Action{Kind: "page", ID: p.Name})
		v := m.View()
		if len(v.Blocks) == 0 || v.Blocks[0].Kind != Heading {
			t.Fatal("page lacks shared heading", p.Name)
		}

	}
	m.Dispatch(Action{Kind: "host", ID: "birch"})
	var kinds []BlockKind
	for _, b := range m.View().Blocks {
		kinds = append(kinds, b.Kind)
	}
	if !reflect.DeepEqual(kinds, []BlockKind{Heading, Identity, Metrics, Tables, Sections}) {
		t.Fatal("host layout order", kinds)
	}
	m.Dispatch(Action{Kind: "task", ID: "network-birch"})
	found := false
	for _, b := range m.View().Blocks {
		if b.Kind == Preview {
			found = true
		}
	}
	if !found {
		t.Fatal("log preview presence must be shared, not adapter-dependent")
	}
}
