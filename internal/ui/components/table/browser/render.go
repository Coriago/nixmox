package browser

import (
	_ "embed"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	actions "nixmox/internal/ui/browser"
	buttons "nixmox/internal/ui/components/button/browser"
	bars "nixmox/internal/ui/components/progress/browser"
	"nixmox/internal/ui/components/table"
)

func Render(t table.Model, dispatch actions.Dispatch) app.UI {
	return app.Section().Class("data-panel", "table-panel").Body(
		app.Div().Class("panel-title").Body(app.H2().Text(t.Title), app.Small().Textf("%d records", len(t.Records))),
		app.Div().Class("table-scroll").Body(app.Table().Body(
			app.THead().Body(app.Tr().Body(app.Range(t.Columns).Slice(func(i int) app.UI { return app.Th().Attr("scope", "col").Text(t.Columns[i]) }))),
			app.TBody().Body(app.Range(t.Records).Slice(func(i int) app.UI { return row(t.Records[i], dispatch) })),
		)),
		app.If(len(t.Records) == 0, func() app.UI { return app.P().Class("empty-state").Text(t.Empty) }),
	)
}

func row(r table.Record, dispatch actions.Dispatch) app.UI {
	row := app.Tr().DataSet("resource", r.ID)
	if r.Action.Kind != "" {
		row = row.Class("data-row").OnClick(actions.Click(dispatch, r.Action), app.EventScope("record", r.Action.Kind, r.Action.ID))
	}
	return row.Body(app.Range(r.Cells).Slice(func(i int) app.UI {
		cell := r.Cells[i]
		valueClass := "cell-value"
		if cell.Badge {
			valueClass += " status-label"
		}
		return app.Td().Class("tone-"+cell.Tone).Body(
			app.If(i == 0, func() app.UI {
				content := []app.UI{app.Span().Class("status-marker", "tone-"+r.Tone).Aria("hidden", true).Text(r.Marker), app.Strong().Text(cell.Text)}
				if r.Action.Kind == "" {
					return app.Span().Class("record-identity").Body(content...)
				}
				return app.Button().Type("button").Class("resource-link").OnClick(func(ctx app.Context, e app.Event) { e.StopImmediatePropagation(); dispatch(ctx, r.Action) }, app.EventScope("link", r.Action.Kind, r.Action.ID)).Body(content...)
			}).Else(func() app.UI { return app.Span().Class(valueClass).Text(cell.Text) }),
			app.If(cell.Bar.Known, func() app.UI { return bars.Render(cell.Bar) }),
			app.If(cell.Detail != "", func() app.UI { return app.Small().Class("cell-detail").Text(cell.Detail) }),
			app.If(i == len(r.Cells)-1 && len(r.Controls) > 0, func() app.UI {
				return app.Div().Class("record-actions").Body(app.Range(r.Controls).Slice(func(n int) app.UI { return buttons.Render(r.Controls[n], dispatch) }))
			}),
		)
	}))
}

//go:embed style.css
var CSS string
