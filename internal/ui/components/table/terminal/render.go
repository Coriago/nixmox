package terminal

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	bars "nixmox/internal/ui/components/progress/terminal"
	"nixmox/internal/ui/components/table"
	"nixmox/internal/ui/components/theme"
	style "nixmox/internal/ui/terminal"
)

func tableColumns(t table.Model, width int) ([]int, []int) {
	n := len(t.Columns)
	cols := make([]int, n)
	for i := range cols {
		cols[i] = i
	}
	width = max(width, 80) // Pan the viewport instead of omitting columns from the shared list.
	space := width - (len(cols)-1)*3
	weights := make([]int, len(cols))
	total := 0
	for i, c := range cols {
		weights[i] = 12
		if c == 0 {
			weights[i] = 22
		}
		if c == 2 {
			weights[i] = 27
		}
		total += weights[i]
	}
	widths := make([]int, len(cols))
	used := 0
	for i := range widths {
		widths[i] = space * weights[i] / total
		used += widths[i]
	}
	widths[0] += space - used
	return cols, widths
}

func Header(them theme.Theme, t table.Model, width int) string {
	p := style.Styles{Theme: them}
	cols, widths := tableColumns(t, width)
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = style.Fit(t.Columns[c], widths[i])
	}
	return lipgloss.NewStyle().Background(lipgloss.Color(p.Theme.Raised)).Foreground(lipgloss.Color(p.Theme.Muted)).Render(strings.Join(parts, " │ "))
}

func Record(them theme.Theme, t table.Model, r table.Record, focused bool, width int) string {
	width = max(width, 80)
	p := style.Styles{Theme: them}
	cols, widths := tableColumns(t, width)
	top, bottom := make([]string, len(cols)), make([]string, len(cols))
	detail := false
	for i, c := range cols {
		cell := r.Cells[c]
		text := cell.Text
		if c == 0 {
			text = r.Marker + " " + text
		}
		top[i] = p.Text(cell.Tone, style.Fit(text, widths[i]))
		if c == 0 {
			top[i] = p.Text(r.Tone, style.Fit(text, widths[i]))
		}
		if cell.Bar.Known {
			bottom[i] = bars.Render(them, cell.Bar, max(1, widths[i]-6)) + p.Text("muted", fmt.Sprintf(" %3.0f%%", cell.Bar.Value*100))
			detail = true
		} else {
			bottom[i] = p.Text("muted", style.Fit(cell.Detail, widths[i]))
			if cell.Detail != "" {
				detail = true
			}
		}
	}
	body := strings.Join(top, " │ ")
	if detail {
		body += "\n" + strings.Join(bottom, "   ")
	}
	s := lipgloss.NewStyle().Width(width).Background(lipgloss.Color(p.Theme.Surface))
	if focused {
		s = s.Background(lipgloss.Color(p.Theme.Raised)).UnderlineStyle(lipgloss.UnderlineSingle).UnderlineColor(lipgloss.Color(p.Theme.Accent))
	}
	return s.Render(body)
}
