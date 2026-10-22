// Package browser is the DOM adapter for the shared progress controller.
package browser

import (
	"fmt"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
	"nixmox/internal/ui/progress"
)

type Component struct {
	app.Compo
	demo *progress.Demo
	stop chan struct{}
}

func Register() { app.Route("/", func() app.Composer { return &Component{} }) }
func (c *Component) OnMount(ctx app.Context) {
	if !app.IsClient {
		return
	}
	c.demo = progress.NewDemo()
	if app.Window().Call("matchMedia", "(prefers-reduced-motion: reduce)").Get("matches").Bool() {
		c.demo.Dispatch(progress.Action{Kind: progress.Motion})
	}
	stop := make(chan struct{})
	c.stop = stop
	ctx.Async(func() {
		ticker := time.NewTicker(progress.FrameInterval)
		defer ticker.Stop()
		last := time.Now()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				delta := now.Sub(last)
				last = now
				ctx.Dispatch(func(app.Context) { c.demo.Advance(delta) })
			}
		}
	})
	ctx.Defer(func(app.Context) { app.Window().Get("document").Call("getElementById", "progress").Call("focus") })
}
func (c *Component) OnDismount() {
	if c.stop != nil {
		close(c.stop)
		c.stop = nil
	}
}
func (c *Component) dispatch(ctx app.Context, a progress.Action) {
	c.demo.Dispatch(a)
	if a.Kind == progress.Next || a.Kind == progress.Previous || a.Kind == progress.Completed {
		id := c.demo.View().Selected
		ctx.Defer(func(app.Context) {
			el := app.Window().Get("document").Call("getElementById", id)
			if el.Truthy() {
				el.Call("focus", map[string]any{"preventScroll": true})
				el.Call("scrollIntoView", map[string]any{"block": "nearest"})
			} else {
				app.Window().Get("document").Call("getElementById", "progress").Call("focus")
			}
		})
	}
}
func (c *Component) click(a progress.Action) app.EventHandler {
	return func(ctx app.Context, _ app.Event) { c.dispatch(ctx, a) }
}
func (c *Component) key(ctx app.Context, e app.Event) {
	key := e.Get("key").String()
	// Let native buttons activate with Enter/Space; all other shared shortcuts
	// bubble to the component. Never intercept browser modifier shortcuts.
	if e.Get("ctrlKey").Bool() || e.Get("metaKey").Bool() || e.Get("altKey").Bool() {
		return
	}
	if (key == "Enter" || key == " ") && e.Get("target").Get("tagName").String() == "BUTTON" {
		return
	}
	if a, ok := progress.Key(key); ok {
		e.PreventDefault()
		c.dispatch(ctx, a)
	}
}
func bar(b progress.Bar) app.UI {
	return app.Div().Class("bar").Role("progressbar").Aria("label", b.Label).Aria("valuemin", 0).Aria("valuemax", 100).Aria("valuenow", fmt.Sprintf("%.0f", b.Value*100)).Body(app.Div().Class("fill").Style("width", fmt.Sprintf("%.2f%%", b.Display*100)))
}
func (c *Component) Render() app.UI {
	if c.demo == nil {
		return app.Main().Class("loading").Text("Loading shared Go progress controller…")
	}
	v := c.demo.View()
	return app.Main().ID("progress").TabIndex(0).OnKeyDown(c.key).DataSet("selected", v.Selected).DataSet("status", v.Status).Body(
		app.Header().Class("masthead").Body(app.Span().Class("brand").Text("nixmox / progress"), app.Span().Class("eyebrow").Text("ONE COMPONENT · TWO RENDERERS")),
		app.Section().Class("monitor").Aria("label", "Build progress preview").Body(
			app.Div().Class("heading").Body(app.Div().Body(app.P().Class("eyebrow").Text("DETERMINISTIC SIMULATION · NO HOST OPERATIONS"), app.H1().Text(v.Title), app.P().Class("subtitle").Text(v.Subtitle)), app.Span().Class("status tone-"+v.Tone).Text(v.Spinner+" "+v.Status)),
			app.Div().Class("overall").Body(app.Div().Class("bar-label").Body(app.Span().Text(v.Overall.Label), app.Span().Text("Completed work, not a time estimate")), bar(v.Overall)),
			app.Div().Class("stats").Body(app.Range(v.Stats).Slice(func(i int) app.UI {
				s := v.Stats[i]
				return app.Div().Class("stat").Body(app.Small().Text(s.Label), app.Strong().Class("tone-"+s.Tone).Text(s.Value), app.Span().Text(s.Detail))
			})),
			app.Div().Class("controls").Body(app.Range(v.Controls).Slice(func(i int) app.UI {
				b := v.Controls[i]
				return app.Button().ID("control-"+b.Action).Type("button").Aria("pressed", b.Active).OnClick(c.click(progress.Action{Kind: b.Action})).Body(app.Text(b.Label), app.Kbd().Text(b.Key))
			})),
			app.Div().Class("section-label").Body(app.Span().Text("ACTIVITY"), app.Span().Text("↑ ↓ select · Enter expand · click a row to inspect")),
			app.Div().Class("activities").Body(app.Range(v.Rows).Slice(func(i int) app.UI {
				r := v.Rows[i]
				class := "activity"
				if r.Selected {
					class += " selected"
				}
				return app.Div().Class(class).DataSet("activity", r.ID).Body(
					app.Button().ID(r.ID).Class("row").Type("button").Aria("expanded", r.Expanded).OnClick(func(ctx app.Context, _ app.Event) {
						c.dispatch(ctx, progress.Action{Kind: progress.Select, ID: r.ID})
						c.dispatch(ctx, progress.Action{Kind: progress.Expand})
					}).Body(
						app.Span().Class("marker tone-"+r.Tone).Text(r.Marker),
						app.Span().Class("identity").Body(app.Strong().Text(r.Name), app.Small().Text(r.Kind+" · "+r.Host)),
						app.Span().Class("measurement").Body(app.Span().Class("tone-"+r.Tone).Text(r.State+" · "+r.Phase), app.If(r.Progress.Known, func() app.UI { return bar(r.Progress) }), app.Small().Text(r.Progress.Label)),
						app.Span().Class("timing").Body(app.Span().Text(r.Elapsed), app.Small().Text(r.Rate)),
					),
					app.If(r.Expanded, func() app.UI { return app.Pre().Class("detail").Text(strings.Join(r.Detail, "\n")) }),
				)
			})),
			app.Div().Class("section-label").Body(app.Span().Text("EVENT FEED"), app.Span().Text("Latest 4 · bounded history")),
			app.Div().Class("feed").Body(app.Range(v.Feed).Slice(func(i int) app.UI {
				e := v.Feed[i]
				return app.Div().Body(app.Time().Text(e.At), app.Span().Class("tone-"+e.Tone).Text(e.Text))
			})),
		),
		app.Footer().Text("Same observations → same Go controller → same view. Only pixels and terminal cells differ."),
	)
}
