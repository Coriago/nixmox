package terminal

import (
	"strings"

	"nixmox/internal/ui/components/build"
	"nixmox/internal/ui/components/theme"
	style "nixmox/internal/ui/terminal"
)

func Render(palette theme.Theme, model build.Model, width int) string {
	styles := style.Styles{Theme: palette}
	lines := []string{styles.Text("accent", "NIX BUILD MONITOR"), model.Summary()}
	rows := model.Rows()
	if len(rows) == 0 {
		lines = append(lines, "Waiting for Nix activity events; totals are not estimated.")
	}
	for _, activity := range rows {
		lines = append(lines, style.Fit(activity.Kind()+" · "+activity.Name, width))
		lines = append(lines, styles.Text("muted", style.Fit("  "+activity.State()+" · "+activity.Progress(), width)))
	}
	if model.Truncated || len(model.Activities) > len(rows) {
		lines = append(lines, "Auxiliary/older activities hidden; showing up to 16 rows.")
	}
	return strings.Join(lines, "\n")
}
