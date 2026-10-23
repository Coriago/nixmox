package browser

import (
	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui"
)

type Dispatch func(app.Context, ui.Action)

func Element(id string) app.Value { return app.Window().Get("document").Call("getElementById", id) }

func Focus(id string) {
	if el := Element(id); el.Truthy() {
		el.Call("focus", map[string]any{"preventScroll": true})
	}
}

func Click(dispatch Dispatch, a ui.Action) app.EventHandler {
	return func(ctx app.Context, _ app.Event) { dispatch(ctx, a) }
}
