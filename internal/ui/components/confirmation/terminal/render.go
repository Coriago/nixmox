package terminal

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"nixmox/internal/ui"
	"nixmox/internal/ui/components/button"
	buttons "nixmox/internal/ui/components/button/terminal"
	"nixmox/internal/ui/components/confirmation"
	"nixmox/internal/ui/components/progress"
	"nixmox/internal/ui/components/theme"
	style "nixmox/internal/ui/terminal"
)

type State struct {
	Choice int
	Spring progress.Spring
}

func (s *State) Frame(reduced bool) { s.Spring.Step(1, reduced) }
func (s *State) Key(key string) ui.Action {
	switch key {
	case "esc":
		return ui.Action{Kind: "dismiss"}
	case "tab", "shift+tab", "left", "right", "up", "down":
		s.Choice = 1 - s.Choice
	case "enter":
		kind := "dismiss"
		if s.Choice == 1 {
			kind = "confirm"
		}
		return ui.Action{Kind: kind}
	}
	return ui.Action{}
}

type View struct {
	Content           string
	CancelY, ConfirmY int
}

func Render(t theme.Theme, c confirmation.Model, choice, width, height int) View {
	p := style.Styles{Theme: t}
	cancel := buttons.Label(t, "Cancel", choice == 0, width-8)
	confirm := buttons.Render(t, button.Model{Label: c.Confirm, Tone: c.Tone()}, choice == 1, width-8)
	bodyLines := strings.Split(ansi.Wrap(c.Body, width-6, ""), "\n")
	budget := max(1, height-13)
	if len(bodyLines) > budget {
		bodyLines = bodyLines[:budget]
		bodyLines[budget-1] = ansi.Truncate(bodyLines[budget-1], width-7, "") + "…"
	}
	prefix := p.Text("accent", "REVIEW") + "\n" + p.Text("text", c.Title) + "\n" + p.Text("running", c.Target) + "\n\n" + strings.Join(bodyLines, "\n") + "\n"
	body := prefix + cancel + "\n" + confirm + "\n\n" + p.Text("muted", "Tab choose · Enter act · Esc dismiss")
	box := lipgloss.NewStyle().Width(width).Padding(1, 2).Background(lipgloss.Color(t.Raised)).Foreground(lipgloss.Color(t.Text)).Border(lipgloss.NormalBorder()).BorderForegroundBlend(lipgloss.Color(t.Accent), lipgloss.Color(t.Running)).Render(body)
	buttonY := 2 + lipgloss.Height(strings.TrimSuffix(prefix, "\n"))
	return View{Content: box, CancelY: buttonY, ConfirmY: buttonY + 1}
}
