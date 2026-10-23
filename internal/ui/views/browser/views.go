package browser

import (
	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui/components/button"
	buttons "nixmox/internal/ui/components/button/browser"
	confirmation "nixmox/internal/ui/components/confirmation/browser"
	logs "nixmox/internal/ui/components/log/browser"
	"nixmox/internal/ui/components/search"
	searches "nixmox/internal/ui/components/search/browser"
	sections "nixmox/internal/ui/components/section/browser"
	tables "nixmox/internal/ui/components/table/browser"
	telemetry "nixmox/internal/ui/components/telemetry/browser"
	"nixmox/internal/ui/views"
)

func (c *Component) Render() app.UI {
	if c.core == nil {
		return app.Main().Class("loading").Text("Opening your workspace…")
	}
	v := c.core.View()
	return app.Div().ID("workspace").Class("workspace").Attr("style", v.Theme.CSS()).
		DataSet("reduced", v.Reduced).DataSet("theme", v.Theme.Name).OnKeyDown(c.key).Body(
		c.sidebar(v),
		app.Div().Class("main-shell").Body(
			app.Header().Class("topbar").Body(
				app.Span().Class("context").Text("homelab"),
				app.Span().Class("preview-badge").Text("MOCK WORKSPACE"),
				app.Div().Class("toolbar").Body(
					buttons.Render(button.Model{Label: "Theme", Action: views.Action{Kind: "theme"}}, c.dispatch),
					buttons.Render(button.Model{Label: pauseLabel(v.Paused), Action: views.Action{Kind: "pause"}, Active: v.Paused}, c.dispatch),
					buttons.Render(button.Model{Label: "Replay", Action: views.Action{Kind: "replay"}}, c.dispatch),
					buttons.Render(button.Model{Label: "?", Action: views.Action{Kind: "help"}}, c.dispatch),
				),
			),
			c.page(v),
			app.Footer().Class("footer").Body(
				app.Span().Text("SIMULATED DATA · NO HOST OPERATIONS"),
				app.Span().Text("t theme · m motion · ? help"),
			),
		),
		confirmation.Render(v.Modal, c.dispatch),
	)
}
func pauseLabel(paused bool) string {
	if paused {
		return "Resume"
	}
	return "Pause"
}
func (c *Component) sidebar(v views.View) app.UI {
	return app.Aside().Class("sidebar").Body(
		app.Div().Class("brand").Body(app.Span().Class("brand-gem").Aria("hidden", true).Text("◈"), app.Strong().Text("nixmox"), app.Small().Text("CLUSTER CONSOLE")),
		app.Div().Class("workspace-label").Body(app.Small().Class("eyebrow").Text("WORKSPACE"), app.Strong().Text("homelab"), app.Small().Text("Cluster workspace")),
		app.Nav().Aria("label", "Main navigation").Body(app.Range(v.Nav).Slice(func(i int) app.UI {
			b := v.Nav[i]
			return app.Button().Type("button").Class("nav-item").Aria("current", b.Active).OnClick(c.click(b.Action), app.EventScope(b.Action.Kind, b.Action.ID)).Body(
				app.Span().Text(b.Label), app.Span().Class("nav-index").Text(i+1))
		})),
		app.Div().Class("host-shortcuts").Body(
			app.Div().Class("section-heading").Body(app.Small().Class("eyebrow").Text("HOSTS"), app.Small().Text("QUICK SWITCH")),
			app.Div().Class("host-shortcut-list").Body(app.Range(v.Hosts).Slice(func(i int) app.UI {
				h := v.Hosts[i]
				return app.Button().Type("button").Class("host-shortcut").Aria("pressed", v.HostID == h.ID).OnClick(c.click(h.Action), app.EventScope(h.Action.Kind, h.Action.ID)).Body(
					app.Span().Class("host-dot", "tone-"+h.Tone).Aria("hidden", true).Text(h.Marker),
					app.Span().Text(h.Title), app.Small().Text(h.State))
			})),
		),
		app.Div().Class("sidebar-bottom").Body(app.Span().Class("connection-dot").Text("○"), app.Div().Body(app.Strong().Text("Preview session"), app.Small().Text("No daemon connected"))),
	)
}

func (c *Component) page(v views.View) app.UI {
	return app.Main().ID("page").Class("page").DataSet("page", v.Page).Body(
		app.Range(v.Blocks).Slice(func(i int) app.UI { return c.block(v, v.Blocks[i]) }),
	)
}
func (c *Component) block(v views.View, b views.Block) app.UI {
	switch b.Kind {
	case views.Heading:
		return app.Header().Class("page-heading").Body(
			app.Div().Class("heading-main").Body(app.P().Class("eyebrow").Text(v.Breadcrumb), app.H1().ID("page-title").TabIndex(-1).Text(v.Title), app.P().Class("subtitle").Text(v.Subtitle)),
			app.Div().Class("header-actions").Body(app.Range(v.HeaderActions).Slice(func(i int) app.UI { return buttons.Render(v.HeaderActions[i], c.dispatch) })),
		)
	case views.Identity:
		return app.Section().Class("identity-panel").Aria("label", "Resource information").Body(sections.Fields(b.Fields))
	case views.Tabs:
		return app.Nav().Class("detail-tabs").Aria("label", "Detail sections").Body(app.Range(b.Buttons).Slice(func(i int) app.UI { return buttons.Render(b.Buttons[i], c.dispatch) }))
	case views.Metrics:
		return telemetry.Render(b.Metrics)
	case views.Search:
		return searches.Render(search.Model{Query: v.Query, Label: v.Page}, c.dispatch)
	case views.Controls:
		return app.Div().Class("page-actions").Body(app.Range(b.Buttons).Slice(func(i int) app.UI { return buttons.Render(b.Buttons[i], c.dispatch) }))
	case views.Sections:
		return app.Div().Class("section-grid").Body(sections.Render(b.Section, c.dispatch))
	case views.Tables:
		return app.Div().Class("tables").Body(tables.Render(b.Table, c.dispatch))
	case views.Preview:
		return logs.Preview(b.Lines)
	case views.Logs:
		return c.logView.Render(*b.Log, c.dispatch)
	case views.Notice:
		return app.P().Class("notice").Role("status").Text(v.Notice)
	}
	return nil
}
