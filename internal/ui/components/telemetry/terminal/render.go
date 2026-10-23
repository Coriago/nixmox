package terminal

import (
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"nixmox/internal/ui/components/progress"
	bars "nixmox/internal/ui/components/progress/terminal"
	"nixmox/internal/ui/components/telemetry"
	"nixmox/internal/ui/components/theme"
	style "nixmox/internal/ui/terminal"
)

func Plot(t theme.Theme, history []float64, width, height int, tone string) string {
	p := style.Styles{Theme: t}
	if len(history) == 0 {
		return p.Text("muted", style.Fit("No live sample", width))
	}
	var lines []string
	for row := height - 1; row >= 0; row-- {
		var line strings.Builder
		for x := range width {
			v := history[x*(len(history)-1)/max(1, width-1)]
			level := int(math.Round((v*float64(height) - float64(row)) * 8))
			level = max(0, min(8, level))
			line.WriteRune([]rune(" ▁▂▃▄▅▆▇█")[level])
		}
		lines = append(lines, p.Text(tone, line.String()))
	}
	return strings.Join(lines, "\n")
}

func Render(t theme.Theme, metrics []telemetry.Model, width int, compact bool) string {
	p := style.Styles{Theme: t}
	cols := 2
	if width >= 80 {
		cols = 4
	}
	cw := (width - (cols - 1)) / cols
	var rows []string
	for start := 0; start < len(metrics); start += cols {
		var tiles []string
		for i := start; i < min(start+cols, len(metrics)); i++ {
			m := metrics[i]
			plotHeight := 2
			if compact {
				plotHeight = 1
			}
			body := p.Text("muted", style.Fit(m.Label, cw-4)) + "\n" + p.Text(m.Tone, style.Fit(m.Value, cw-4)) + "\n" + p.Text("muted", style.Fit(m.Detail, cw-4)) + "\n"
			if m.Visualization() == telemetry.History {
				body += Plot(t, m.History, cw-4, plotHeight, m.Tone)
			} else if m.Visualization() == telemetry.Capacity {
				body += bars.Render(t, progress.Bar{Display: m.Fraction}, cw-4) + strings.Repeat("\n", plotHeight-1)
			} else {
				body += p.Text("muted", style.Fit("Unavailable", cw-4)) + strings.Repeat("\n", plotHeight-1)
			}
			tiles = append(tiles, p.Panel(body, cw))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, joinSpaced(tiles)...))
	}
	return strings.Join(rows, "\n")
}

func joinSpaced(items []string) []string {
	var out []string
	for i, s := range items {
		if i > 0 {
			out = append(out, " ")
		}
		out = append(out, s)
	}
	return out
}
