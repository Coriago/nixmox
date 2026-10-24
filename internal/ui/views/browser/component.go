// Package browser owns application lifecycle and renders the shared view layout.
package browser

import (
	"context"
	"time"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/client"
	actions "nixmox/internal/ui/browser"
	confirmation "nixmox/internal/ui/components/confirmation/browser"
	logs "nixmox/internal/ui/components/log/browser"
	"nixmox/internal/ui/components/progress"
	"nixmox/internal/ui/views"
)

type Component struct {
	app.Compo
	core         *views.Model
	cancel       context.CancelFunc
	connection   *views.Connection
	selection    views.Selection
	logView      logs.View
	confirmation confirmation.Controller
}

func Register() { app.Route("/", func() app.Composer { return &Component{} }) }
func (c *Component) OnMount(ctx app.Context) {
	if !app.IsClient {
		return
	}
	c.core = views.New()
	c.logView.FollowState = &c.core.LogFollow
	if app.Window().Call("matchMedia", "(prefers-reduced-motion: reduce)").Get("matches").Bool() {
		c.core.Reduced = true
	}
	life, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	api, err := client.New(app.Window().Get("location").Get("origin").String(), nil)
	if err != nil {
		c.core.Apply(views.Update{Err: err})
		return
	}
	c.connection = views.NewConnection(api)
	ctx.Async(func() {
		c.connection.Run(life, func(update views.Update) {
			ctx.Dispatch(func(ctx app.Context) {
				if life.Err() != nil {
					return
				}
				old := c.core.Route()
				c.core.Apply(update)
				c.exchange()
				if old != c.core.Route() {
					ctx.Defer(func(app.Context) { actions.Focus("page-title") })
				}
				c.followTail(ctx)
			})
		})
	})
	ctx.Async(func() {
		t := time.NewTicker(progress.FrameInterval)
		defer t.Stop()
		last := time.Now()
		for {
			select {
			case <-life.Done():
				return
			case now := <-t.C:
				dt := now.Sub(last)
				last = now
				ctx.Dispatch(func(ctx app.Context) {
					if life.Err() != nil {
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
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}
func (c *Component) followTail(ctx app.Context) {
	if c.core.View().Find(views.Logs) != nil {
		c.logView.ScrollToTail(ctx)
	}
}
func (c *Component) dispatch(ctx app.Context, a views.Action) {
	if a.Kind == "search-focus" && c.core.Modal == nil {
		if c.core.View().Find(views.Search) != nil {
			actions.Focus("record-search")
		}
		return
	}
	hadModal := c.core.Modal != nil
	if hadModal && (a.Kind == "dismiss" || a.Kind == "confirm") {
		c.confirmation.Close(ctx)
	}
	old := c.core.Route()
	c.core.Dispatch(a)
	c.exchange()
	c.followTail(ctx)
	if old != c.core.Route() {
		ctx.Defer(func(app.Context) { actions.Focus("page-title") })
	}
	if !hadModal && c.core.Modal != nil {
		c.confirmation.Open(ctx)
	}
}
func (c *Component) exchange() {
	if c.connection == nil {
		return
	}
	if selection := c.core.Selection(); selection != c.selection {
		c.selection = selection
		c.connection.Select(selection)
	}
	if command := c.core.TakeCommand(); command != nil {
		c.connection.Submit(*command)
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
