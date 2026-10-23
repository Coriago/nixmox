package terminal

import (
	"strings"

	"nixmox/internal/ui/components/section"
	"nixmox/internal/ui/components/theme"
	style "nixmox/internal/ui/terminal"
)

func Fields(t theme.Theme, fields []section.Field, width int) string {
	p := style.Styles{Theme: t}
	cols := 2
	if width >= 85 {
		cols = 4
	}
	cw := (width - (cols-1)*2) / cols
	var lines []string
	for start := 0; start < len(fields); start += cols {
		var labels, values []string
		for i := start; i < min(start+cols, len(fields)); i++ {
			f := fields[i]
			labels = append(labels, p.Text("muted", style.Fit(f.Label, cw)))
			values = append(values, p.Text(f.Tone, style.Fit(f.Value, cw)))
		}
		lines = append(lines, strings.Join(labels, "  "), strings.Join(values, "  "))
	}
	return strings.Join(lines, "\n")
}
