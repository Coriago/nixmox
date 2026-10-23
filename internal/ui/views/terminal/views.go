package terminal

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"nixmox/internal/ui/components/button"
	buttons "nixmox/internal/ui/components/button/terminal"
	confirmation "nixmox/internal/ui/components/confirmation/terminal"
	"nixmox/internal/ui/components/log"
	logs "nixmox/internal/ui/components/log/terminal"
	sections "nixmox/internal/ui/components/section/terminal"
	"nixmox/internal/ui/components/table"
	tables "nixmox/internal/ui/components/table/terminal"
	telemetry "nixmox/internal/ui/components/telemetry/terminal"
	style "nixmox/internal/ui/terminal"
	"nixmox/internal/ui/views"
)

type target struct {
	action     views.Action
	x, y, w, h int
	content    bool
	row        bool
}
type page struct {
	text    string
	targets []target
}

func (m *Model) contentPage() page {
	v := m.view
	p := style.Styles{Theme: v.Theme}
	w := m.content.Width()
	var lines []string
	var targets []target
	y := 0
	add := func(s string) { lines = append(lines, s); y += lipgloss.Height(s) }
	controls := func(items []button.Model, right bool) {
		x, total := 0, 0
		var row []string
		for _, b := range items {
			total += min(w, lipgloss.Width(b.Label)+2) + 1
		}
		if right && total < w {
			x = w - total
		}
		prefix := strings.Repeat(" ", x)
		for _, b := range items {
			bw := min(w, lipgloss.Width(b.Label)+2)
			if x+bw > w && len(row) > 0 {
				add(prefix + strings.Join(row, " "))
				row = nil
				x = 0
				prefix = ""
			}
			if !b.Disabled {
				targets = append(targets, target{action: b.Action, x: x, y: y, w: bw, h: 1, content: true})
			}
			row = append(row, buttons.Render(v.Theme, b, m.focus == b.Action, bw))
			x += bw + 1
		}
		if len(row) > 0 {
			add(prefix + strings.Join(row, " "))
		}
	}
	drawTable := func(t table.Model) {
		add(p.Text("accent", style.Fit(strings.ToUpper(t.Title)+" / "+fmt.Sprint(len(t.Records)), w)))
		add(tables.Header(v.Theme, t, w))
		for _, r := range t.Records {
			body := tables.Record(v.Theme, t, r, r.Action.Kind != "" && m.focus == r.Action, w)
			if r.Action.Kind != "" {
				targets = append(targets, target{action: r.Action, y: y, w: w, h: lipgloss.Height(body), content: true, row: true})
			}
			add(body)
			controls(r.Controls, true)
		}
		if len(t.Records) == 0 {
			add(p.Text("muted", style.Fit(t.Empty, w)))
		}
		add("")
	}
	for _, block := range v.Blocks {
		switch block.Kind {
		case views.Heading:
			add(p.Text("muted", style.Fit(v.Breadcrumb, w)))
			add(p.Text("accent", style.Fit(v.Title, w)))
			add(p.Text("muted", style.Fit(v.Subtitle, w)))
			controls(v.HeaderActions, true)
		case views.Identity:
			add(sections.Fields(v.Theme, block.Fields, w))
		case views.Tabs:
			controls(block.Buttons, false)
		case views.Metrics:
			add(p.Text("muted", "TELEMETRY · MOCK RECENT SAMPLES"))
			add(telemetry.Render(v.Theme, block.Metrics, w, m.height < 30))
		case views.Search:
			if m.search.Editing {
				add(ansi.Truncate(m.search.Input.View(), w, "…"))
			} else {
				label := "/ Filter records"
				if v.Query != "" {
					label = "/ " + v.Query
				}
				controls([]button.Model{{Label: label, Action: views.Action{Kind: "search-focus"}}}, false)
			}
		case views.Controls:
			controls(block.Buttons, false)
		case views.Tables:
			drawTable(block.Table)
		case views.Sections:
			s := block.Section
			add(p.Text("accent", style.Fit(strings.ToUpper(s.Title), w)))
			if len(s.Fields) > 0 {
				add(sections.Fields(v.Theme, s.Fields, w))
			}
			if s.Note != "" {
				add(p.Text("muted", ansi.Wrap(s.Note, w, "")))
			}
			controls(s.Controls, false)
			add("")
		case views.Preview:
			add(logs.Preview(v.Theme, block.Lines, w))
		case views.Logs:
			label := log.FollowLabel(m.logView.Follow)
			controls([]button.Model{{Label: label, Action: views.Action{Kind: "follow"}}}, false)
			add(logs.Render(v.Theme, *block.Log, w))
		case views.Notice: // Docked in the terminal status footer by layout().
		}
	}
	return page{strings.Join(lines, "\n"), targets}
}

func (m *Model) layout() (tea.View, []target) {
	v := m.view
	p := style.Styles{Theme: v.Theme}
	bg := lipgloss.NewStyle().Width(m.width).Height(m.height).Background(lipgloss.Color(v.Theme.Background)).Render("")
	layers := []*lipgloss.Layer{lipgloss.NewLayer(bg)}
	var targets []target
	put := func(s string, x, y int) { layers = append(layers, lipgloss.NewLayer(s).X(x).Y(y)) }
	put(ansi.Truncate(p.Text("accent", "nixmox")+"  "+p.Text("muted", "/ homelab"), max(1, m.width-4), "…"), 2, 1)
	badge := "MOCK WORKSPACE · no host operations"
	if v.Paused {
		badge = "MOCK WORKSPACE · paused"
	}
	put(p.Text("muted", ansi.Truncate(badge, max(1, m.width-4), "…")), 2, 2)
	if m.width < 52 || m.height < 14 {
		put(p.Text("warning", ansi.Truncate("Resize to at least 52 × 14. q to quit.", max(1, m.width-4), "…")), 2, min(5, m.height-1))
		view := tea.NewView(lipgloss.NewCompositor(layers...).Render())
		view.AltScreen = true
		view.BackgroundColor = lipgloss.Color(v.Theme.Background)
		return view, nil
	}
	side := m.sidebarWidth()
	put(lipgloss.NewStyle().Width(side).Height(max(1, m.height-7)).Background(lipgloss.Color(v.Theme.Surface)).BorderRight(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(v.Theme.Border)).Render(""), 0, 4)
	y, navStep := 5, 2
	if m.height < 30 {
		navStep = 1
	}
	for _, b := range v.Nav {
		label := b.Label
		put(buttons.Label(v.Theme, label, m.focus == b.Action || b.Active, side-2), 1, y)
		targets = append(targets, target{action: b.Action, x: 1, y: y, w: side - 2, h: 1})
		y += navStep
	}
	// Shortcuts use available sidebar space; the Hosts page always exposes all hosts.
	if side > 0 {
		put(p.Text("muted", ansi.Truncate("HOSTS / QUICK SWITCH", side-3, "…")), 2, y+1)
		y += 3
		for _, h := range v.Hosts {
			if y >= m.height-4 {
				break
			}
			label := ansi.Truncate(p.Text(h.Tone, h.Marker+" ")+h.Title+" "+p.Text("muted", h.State), side-4, "…")
			style := lipgloss.NewStyle().Width(side-2).Background(lipgloss.Color(v.Theme.Surface)).Padding(0, 1)
			if m.Core.HostID == h.ID || m.focus == h.Action {
				style = style.Background(lipgloss.Color(v.Theme.Raised)).Foreground(lipgloss.Color(v.Theme.Accent))
			}
			put(style.Render(label), 1, y)
			targets = append(targets, target{action: h.Action, x: 1, y: y, w: side - 2, h: 1})

			y += 2
		}
	}

	x := side + 2
	put(m.content.View(), x, 4)
	pg := m.contentPage()
	for _, t := range pg.targets {
		t.x += x
		t.y += 4 - m.content.YOffset()
		targets = append(targets, t)
	}
	footer := "/ filter · j/k select · Enter open · ↑↓ scroll · ←→ pan · Tab focus · t theme · q quit"
	put(p.Text("muted", ansi.Truncate(footer, m.width-4, "…")), 2, m.height-2)
	if v.Notice != "" {
		put(p.Text("accent", ansi.Truncate(v.Notice, m.width-4, "…")), 2, m.height-3)
	}
	if v.Modal != nil {
		w := min(64, m.width-4)
		dialog := confirmation.Render(v.Theme, *v.Modal, m.modal.Choice, w, m.height)
		h := lipgloss.Height(dialog.Content)
		x := (m.width - w) / 2
		y := min(max(0, (m.height-h)/2)+int((1-m.modal.Spring.Position)*2), max(0, m.height-h))
		put(dialog.Content, x, y)
		targets = []target{
			{action: views.Action{Kind: "dismiss"}, x: x + 3, y: y + dialog.CancelY, w: w - 6, h: 1},
			{action: views.Action{Kind: "confirm"}, x: x + 3, y: y + dialog.ConfirmY, w: w - 6, h: 1},
		}
	}
	view := tea.NewView(lipgloss.NewCompositor(layers...).Render())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.BackgroundColor = lipgloss.Color(v.Theme.Background)
	view.ForegroundColor = lipgloss.Color(v.Theme.Text)
	view.WindowTitle = "nixmox · Mock workspace"
	return view, targets
}
