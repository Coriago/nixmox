package browser

import (
	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui"
	actions "nixmox/internal/ui/browser"
)

// Controller owns DOM modality and focus restoration, not approval policy.
type Controller struct{ opener app.Value }

func (c *Controller) Open(ctx app.Context) {
	c.opener = app.Window().Get("document").Get("activeElement")
	ctx.Defer(func(app.Context) {
		actions.Element("confirmation").Call("showModal")
		actions.Focus("dialog-cancel")
	})
}
func (c *Controller) Close(ctx app.Context) {
	actions.Element("confirmation").Call("close")
	ctx.Defer(func(app.Context) {
		if c.opener.Truthy() && c.opener.Get("isConnected").Bool() {
			c.opener.Call("focus")
		} else {
			actions.Focus("page-title")
		}
	})
}
func Key(ctx app.Context, e app.Event, dispatch actions.Dispatch) {
	switch e.Get("key").String() {
	case "Tab":
		e.PreventDefault()
		e.StopImmediatePropagation()
		doc := app.Window().Get("document")
		id := "dialog-cancel"
		if doc.Get("activeElement").Get("id").String() == id {
			id = "dialog-confirm"
		}
		actions.Focus(id)
	case "Escape":
		e.PreventDefault()
		e.Call("stopPropagation")
		dispatch(ctx, ui.Action{Kind: "dismiss"})
	}
}
