// Package browser owns application lifecycle and renders the shared view layout.
package browser

import (
	"time"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	actions "nixmox/internal/ui/browser"
	confirmation "nixmox/internal/ui/components/confirmation/browser"
	logs "nixmox/internal/ui/components/log/browser"
	"nixmox/internal/ui/components/progress"
	"nixmox/internal/ui/views"
)

type Component struct {
	app.Compo
	core         *views.Model
	stop         chan struct{}
	logView      logs.View
	confirmation confirmation.Controller
}

func Register() { app.Route("/", func() app.Composer { return &Component{} }) }
func (c *Component) OnMount(ctx app.Context) {
	if !app.IsClient {
		return
	}
	c.core = views.New()
	c.logView.Follow = true
	if app.Window().Call("matchMedia", "(prefers-reduced-motion: reduce)").Get("matches").Bool() {
		c.core.Reduced = true
	}
	stop := make(chan struct{})
	c.stop = stop
	ctx.Async(func() {
		t := time.NewTicker(progress.FrameInterval)
		defer t.Stop()
		last := time.Now()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				dt := now.Sub(last)
				last = now
				ctx.Dispatch(func(ctx app.Context) {
					if c.core == nil {
						return
					}
					c.core.Advance(dt)
					c.followTail(ctx)
				})
			}
		}
	})
}
func (c *Component) OnDismount() {
	if c.stop != nil {
		close(c.stop)
		c.stop = nil
	}
}
func (c *Component) followTail(ctx app.Context) {
	if c.core.View().Find(views.Logs) != nil {
		c.logView.ScrollToTail(ctx)
	}
}
func (c *Component) dispatch(ctx app.Context, a views.Action) {
	if a.Kind == "follow" {
		c.logView.Follow = true
		c.followTail(ctx)
		return
	}
	hadModal := c.core.Modal != nil
	if hadModal && (a.Kind == "dismiss" || a.Kind == "confirm") {
		c.confirmation.Close(ctx)
	}
	old := c.core.Route()
	c.core.Dispatch(a)
	if old != c.core.Route() {
		c.logView.Follow = true
		ctx.Defer(func(app.Context) { actions.Focus("page-title") })
	}
	if !hadModal && c.core.Modal != nil {
		c.confirmation.Open(ctx)
	}
}
func (c *Component) click(a views.Action) app.EventHandler { return actions.Click(c.dispatch, a) }

func (c *Component) key(ctx app.Context, e app.Event) {
	if c.core == nil {
		return
	}
	k := e.Get("key").String()
	if c.core.Modal != nil {
		confirmation.Key(ctx, e, c.dispatch)
		return
	}
	if e.Get("ctrlKey").Bool() || e.Get("metaKey").Bool() || e.Get("altKey").Bool() {
		return
	}
	switch e.Get("target").Get("tagName").String() {
	case "INPUT", "TEXTAREA", "SELECT":
		return
	}
	if a, ok := views.Shortcut(k); ok {
		e.PreventDefault()
		c.dispatch(ctx, a)
	}
}
