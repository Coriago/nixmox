package browser

import (
	_ "embed"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui"
	actions "nixmox/internal/ui/browser"
	"nixmox/internal/ui/components/search"
)

func Render(model search.Model, dispatch actions.Dispatch) app.UI {
	return app.Div().Class("search-bar").Body(app.Input().Type("search").Class("query-input").Aria("label", "Filter "+model.Label).Placeholder(search.Placeholder).Value(model.Query).OnInput(func(ctx app.Context, e app.Event) {
		dispatch(ctx, ui.Action{Kind: "search", ID: e.Get("target").Get("value").String()})
	}, app.EventScope("search", model.Label)))
}

//go:embed style.css
var CSS string
