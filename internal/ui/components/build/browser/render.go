package browser

import (
	_ "embed"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui/components/build"
)

//go:embed style.css
var CSS string

func Render(model build.Model) app.UI {
	rows := model.Rows()
	return app.Section().Class("build-monitor").Aria("label", "Nix build monitor").Body(
		app.H2().Text("Nix build monitor"),
		app.P().Role("status").Text(model.Summary()),
		app.If(len(rows) == 0, func() app.UI {
			return app.P().Text("Waiting for retained Nix activity events. Progress is not estimated.")
		}),
		app.Div().Class("build-activities").Body(app.Range(rows).Slice(func(i int) app.UI {
			activity := rows[i]
			return app.Article().Class("build-activity").Body(
				app.Strong().Text(activity.Kind()),
				app.Code().Text(activity.Name),
				app.Span().Text(activity.State()),
				app.Span().Text(activity.Progress()),
				app.If(activity.Expected > 0, func() app.UI {
					return app.Progress().Max(float64(activity.Expected)).Value(float64(min(activity.Done, activity.Expected)))
				}),
			)
		})),
		app.If(model.Truncated || len(model.Activities) > len(rows), func() app.UI {
			return app.P().Text("Auxiliary and older activities hidden; up to 16 build/download rows shown.")
		}),
	)
}
