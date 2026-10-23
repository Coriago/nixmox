package browser

import (
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	logs "nixmox/internal/ui/components/log"
	"nixmox/internal/ui/components/table"
	"nixmox/internal/ui/components/telemetry"
	"nixmox/internal/ui/views"
)

func TestNewSharedPageAndListNeedNoBrowserRegistration(t *testing.T) {
	original := views.Pages
	defer func() { views.Pages = original }()
	views.Pages = append(append([]views.Page(nil), views.Pages...), views.Page{Name: "Example", Build: func(_ *views.Model, v *views.View) {
		v.Add(
			views.Block{Kind: views.Metrics, Metrics: []telemetry.Model{{Label: "Shared graph", Known: true, History: []float64{.2, .5, .3}}}},
			views.Block{Kind: views.Tables, Table: table.Model{Title: "Another list", Columns: []string{"Name"}, Records: []table.Record{{ID: "row", Marker: "⠋", Tone: "running", Cells: []table.Cell{{Text: "Shared row"}}}}}},
			views.Block{Kind: views.Logs, Log: &logs.Model{Lines: []string{"Shared log"}, Total: 1}},
		)
	}})
	m := views.New()
	a, _ := views.Shortcut("5")
	m.Dispatch(a)
	c := &Component{core: m}
	html := app.HTMLString(c.Render())
	for _, part := range []string{"Example", "Another list", "Shared row", "Shared graph", "metric-chart", "⠋", "Shared log"} {
		if !strings.Contains(html, part) {
			t.Fatal("generic renderer did not render shared page", part)
		}
	}
}
