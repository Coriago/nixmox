package browser

import (
	_ "embed"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui"
	actions "nixmox/internal/ui/browser"
	"nixmox/internal/ui/components/confirmation"
)

func Render(model *confirmation.Model, dispatch actions.Dispatch) app.UI {
	tone := "primary"
	if model != nil {
		tone = model.Tone()
	}
	return app.Dialog().ID("confirmation").Class("confirmation").Aria("labelledby", "dialog-title").Aria("describedby", "dialog-body").OnKeyDown(func(ctx app.Context, e app.Event) { Key(ctx, e, dispatch) }).Body(
		app.If(model != nil, func() app.UI {
			d := model
			return app.Div().Body(app.Small().Class("eyebrow").Text("REVIEW / SIMULATION"), app.H2().ID("dialog-title").Text(d.Title),
				app.P().Class("dialog-target").Text(d.Target), app.P().ID("dialog-body").Text(d.Body),
				app.Div().Class("dialog-actions").Body(
					app.Button().ID("dialog-cancel").Type("button").Class("button").OnClick(actions.Click(dispatch, ui.Action{Kind: "dismiss"})).Text("Cancel"),
					app.Button().ID("dialog-confirm").Type("button").Class("button", "button-"+tone).OnClick(actions.Click(dispatch, ui.Action{Kind: "confirm"})).Text(d.Confirm)),
				app.Small().Text("Mock preview. No real host operations."))
		}),
	)
}

//go:embed style.css
var CSS string
