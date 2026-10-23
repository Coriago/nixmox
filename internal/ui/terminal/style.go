package terminal

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"nixmox/internal/ui/components/theme"
)

type Styles struct{ Theme theme.Theme }

func (p Styles) Text(tone, s string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Theme.Color(tone))).Render(s)
}

func (p Styles) Panel(body string, width int) string {
	return lipgloss.NewStyle().Width(width).Padding(0, 1).Background(lipgloss.Color(p.Theme.Surface)).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(p.Theme.Border)).Render(body)
}

func Fit(s string, w int) string {
	s = ansi.Truncate(s, max(1, w), "…")
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}
