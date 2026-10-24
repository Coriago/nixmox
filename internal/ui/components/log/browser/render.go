package browser

import (
	_ "embed"
	"strings"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	actions "nixmox/internal/ui/browser"
	buttons "nixmox/internal/ui/components/button/browser"
	logs "nixmox/internal/ui/components/log"
)

type View struct{ *logs.FollowState }

func (v *View) Render(data logs.Model, dispatch actions.Dispatch) app.UI {
	return app.Section().Class("log-panel").Body(
		app.Div().Class("section-heading").Body(app.H2().Class("eyebrow").Text("EXECUTION LOG"), buttons.Render(v.Control(), dispatch)),
		app.Pre().ID("task-log").TabIndex(0).Aria("label", "Task execution log").OnScroll(func(_ app.Context, e app.Event) {
			el := e.Get("target")
			v.Scrolled(el.Get("scrollHeight").Int()-el.Get("scrollTop").Int()-el.Get("clientHeight").Int() < 12)
		}).Text(strings.Join(data.Lines, "\n")),
		app.Small().Text(data.Summary()),
	)
}
func (v *View) ScrollToTail(ctx app.Context) {
	if !v.Follow {
		return
	}
	ctx.Defer(func(app.Context) {
		if !v.Follow {
			return
		}
		el := app.Window().Get("document").Call("getElementById", "task-log")
		if el.Truthy() {
			el.Set("scrollTop", el.Get("scrollHeight"))
		}
	})
}
func Preview(lines []string) app.UI {
	return app.Section().Class("data-panel", "live-preview").Body(app.H2().Class("panel-title").Text("Latest output"), app.Pre().Text(strings.Join(lines, "\n")))
}

//go:embed style.css
var CSS string
