package terminal

import (
	bubbles "charm.land/bubbles/v2/progress"
	"charm.land/lipgloss/v2"

	"nixmox/internal/ui/components/progress"
	"nixmox/internal/ui/components/theme"
)

func Render(t theme.Theme, b progress.Bar, width int) string {
	bar := bubbles.New(bubbles.WithWidth(max(1, width)), bubbles.WithoutPercentage(), bubbles.WithColors(lipgloss.Color(t.Accent), lipgloss.Color(t.Running)))
	bar.EmptyColor = lipgloss.Color(t.Border)
	return bar.ViewAs(b.Display)
}
