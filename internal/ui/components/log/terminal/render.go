package terminal

import (
	"strings"

	"charm.land/bubbles/v2/viewport"

	logs "nixmox/internal/ui/components/log"
	"nixmox/internal/ui/components/theme"
	style "nixmox/internal/ui/terminal"
)

type View struct{ *logs.FollowState }

func (v *View) Scrolled(port viewport.Model) { v.FollowState.Scrolled(port.AtBottom()) }
func (v *View) Sync(port *viewport.Model) {
	if v.Follow {
		port.GotoBottom()
	}
}
func Render(t theme.Theme, data logs.Model, width int) string {
	p := style.Styles{Theme: t}
	lines := []string{p.Text("muted", data.Summary())}
	for _, line := range data.Lines {
		lines = append(lines, p.Text("muted", style.Fit(line, width)))
	}
	return strings.Join(lines, "\n")
}
func Preview(t theme.Theme, lines []string, width int) string {
	p := style.Styles{Theme: t}
	out := []string{p.Text("accent", "LATEST OUTPUT")}
	for _, line := range lines {
		out = append(out, p.Text("muted", style.Fit(line, width)))
	}
	return strings.Join(out, "\n")
}
