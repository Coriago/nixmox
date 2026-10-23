package browser

import (
	_ "embed"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	actions "nixmox/internal/ui/browser"
	buttons "nixmox/internal/ui/components/button/browser"
	"nixmox/internal/ui/components/section"
)

func Fields(items []section.Field) app.UI {
	return app.Dl().Class("fields").Body(app.Range(items).Slice(func(i int) app.UI {
		f := items[i]
		return app.Div().Body(app.Dt().Text(f.Label), app.Dd().Class("tone-"+f.Tone).Text(f.Value))
	}))
}

func Render(s section.Model, dispatch actions.Dispatch) app.UI {
	return app.Section().Class("data-panel").Body(
		app.H2().Class("panel-title").Text(s.Title), Fields(s.Fields),
		app.If(s.Note != "", func() app.UI { return app.P().Class("panel-note").Text(s.Note) }),
		app.If(len(s.Controls) > 0, func() app.UI {
			return app.Div().Class("panel-actions").Body(app.Range(s.Controls).Slice(func(i int) app.UI { return buttons.Render(s.Controls[i], dispatch) }))
		}),
	)
}

//go:embed style.css
var CSS string
