package terminal

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/theme"
)

func Label(t theme.Theme, label string, selected bool, width int) string {
	s := lipgloss.NewStyle().Width(width).Padding(0, 1).Background(lipgloss.Color(t.Raised)).Foreground(lipgloss.Color(t.Text))
	if selected {
		s = s.Background(lipgloss.Color(t.Accent)).Foreground(lipgloss.Color(t.Background)).Bold(true)
	}
	return s.Render(ansi.Truncate(label, max(1, width-2), "…"))
}

func Render(t theme.Theme, b button.Model, focused bool, width int) string {
	if b.Tone == "success" && !b.Disabled {
		return lipgloss.NewStyle().Width(width).Padding(0, 1).Bold(true).Background(lipgloss.Color(t.Success)).Foreground(lipgloss.Color("#181818")).Underline(focused).Render(ansi.Truncate(b.Label, width-2, "…"))
	}
	if b.Tone == "danger" && !b.Disabled {
		return lipgloss.NewStyle().Width(width).Padding(0, 1).Bold(true).Background(lipgloss.Color(t.Danger)).Foreground(lipgloss.Color("#ffffff")).Underline(focused).Render(ansi.Truncate(b.Label, width-2, "…"))
	}
	if b.Disabled {
		return lipgloss.NewStyle().Width(width).Padding(0, 1).Foreground(lipgloss.Color(t.Muted)).Render(ansi.Truncate(b.Label+" ×", width-2, "…"))
	}
	return Label(t, b.Label, focused || b.Active, width)
}
