package browser

import (
	_ "embed"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui"
	actions "nixmox/internal/ui/browser"
	"nixmox/internal/ui/components/search"
)

func Render(model search.Model, dispatch actions.Dispatch) app.UI {
	return app.Div().Class("search-bar").Body(app.Input().ID("record-search").Type("search").Class("query-input").Aria("label", "Filter "+model.Label).Placeholder(search.Placeholder).Value(model.Query).OnInput(func(ctx app.Context, e app.Event) {
		dispatch(ctx, ui.Action{Kind: "search", ID: e.Get("target").Get("value").String()})
	}, app.EventScope("search", model.Label)).OnKeyDown(func(ctx app.Context, e app.Event) {
		key := e.Get("key").String()
		if key != "Escape" && key != "Enter" {
			return
		}
		e.PreventDefault()
		e.StopImmediatePropagation()
		if key == "Escape" {
			dispatch(ctx, ui.Action{Kind: "search"})
		}
		e.Get("target").Call("blur")
	}, app.EventScope("search-key", model.Label)))
}

//go:embed style.css
var CSS string
