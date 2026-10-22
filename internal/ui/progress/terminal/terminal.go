// Package terminal only translates keys/ticks and renders the shared View.
package terminal

import (
	"io"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"nixmox/internal/ui/progress"
)

type Tick time.Time
type Model struct {
	Demo          *progress.Demo
	width, height int
	last          time.Time
	styles        map[string]lipgloss.Style
}

func New(demo *progress.Demo, output io.Writer) *Model {
	renderer := lipgloss.NewRenderer(output)
	m := &Model{Demo: demo, width: 100, height: 38, styles: map[string]lipgloss.Style{}}
	for _, t := range progress.Palette() {
		m.styles[t.Name] = renderer.NewStyle().Foreground(lipgloss.Color(t.Color))
	}
	return m
}
func tick() tea.Cmd {
	return tea.Tick(progress.FrameInterval, func(t time.Time) tea.Msg { return Tick(t) })
}
func (m *Model) Init() tea.Cmd { return tick() }
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case Tick:
		delta := progress.FrameInterval
		if !m.last.IsZero() {
			delta = time.Time(msg).Sub(m.last)
		}
		m.last = time.Time(msg)
		m.Demo.Advance(delta)
		return m, tick()
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		key := msg.String()
		if key == "q" || key == "ctrl+c" || key == "esc" {
			return m, tea.Quit
		}
		if a, ok := progress.Key(key); ok {
			m.Demo.Dispatch(a)
		}
	}
	return m, nil
}
func (m *Model) text(role, s string) string { return m.styles[role].Render(s) }
func (m *Model) bar(b progress.Bar, width int, role string) string {
	width = max(1, width)
	n := min(width, max(0, int(math.Round(b.Display*float64(width)))))
	return m.text(role, strings.Repeat("━", n)) + m.text("line", strings.Repeat("─", width-n))
}
func (m *Model) View() string {
	v := m.Demo.View()
	width := max(10, min(120, m.width-4))
	height := max(5, m.height-1)
	lines := []string{
		m.text("accent", "nixmox / progress") + m.text("muted", "   DETERMINISTIC SIMULATION · no host operations"),
		"",
		m.text("text", v.Title) + "  " + m.text(v.Tone, v.Spinner+" "+v.Status) + "  " + m.text("muted", v.Elapsed),
		m.text("muted", v.Subtitle),
		m.bar(v.Overall, width, "accent"),
		m.text("muted", v.Overall.Label+" · completed work, not a time estimate"),
	}
	for _, s := range v.Stats {
		lines = append(lines, m.text(s.Tone, s.Label+"  "+s.Value)+m.text("muted", "   "+s.Detail))
	}
	var controls []string
	for _, c := range v.Controls {
		role := "muted"
		if c.Active {
			role = "accent"
		}
		controls = append(controls, m.text(role, c.Key+" "+c.Label))
	}
	// Two rows keep all controls visible in ordinary terminal widths.
	lines = append(lines, "", strings.Join(controls[:3], "  ·  "), strings.Join(controls[3:], "  ·  "), m.text("muted", "ACTIVITY  ·  ↑/↓ select · Enter expand"))
	budget := max(2, height-len(lines)-7)
	selected := 0
	for i, r := range v.Rows {
		if r.Selected {
			selected = i
		}
	}
	start := selected
	if len(v.Rows) > 0 {
		used := min(budget, 2+len(v.Rows[selected].Detail))
		for start > 0 && used+2+len(v.Rows[start-1].Detail) <= budget {
			start--
			used += 2 + len(v.Rows[start].Detail)
		}
	}
	for i := start; i < len(v.Rows) && budget >= 2; i++ {
		r := v.Rows[i]
		cursor := " "
		if r.Selected {
			cursor = "›"
		}
		nameWidth := max(12, min(29, width/3))
		name := ansi.Truncate(r.Name, nameWidth, "…")
		name += strings.Repeat(" ", max(0, nameWidth-lipgloss.Width(name)))
		line := cursor + " " + r.Marker + " " + name + "  " + r.State + " · " + r.Phase
		lines = append(lines, m.text(r.Tone, line))
		info := r.Kind + " · " + r.Host + " · " + r.Elapsed
		if r.Progress.Known {
			info = m.bar(r.Progress, 12, r.Tone) + " " + r.Progress.Label + " · " + r.Rate + " · " + r.Elapsed + " · " + r.Host
		}
		lines = append(lines, "    "+m.text("muted", info))
		budget -= 2
		if r.Expanded {
			for _, detail := range r.Detail {
				if budget == 0 {
					break
				}
				lines = append(lines, "    "+m.text("muted", "│ "+detail))
				budget--
			}
		}
	}
	lines = append(lines, m.text("muted", "EVENT FEED · latest 4"))
	for _, e := range v.Feed {
		lines = append(lines, m.text("muted", e.At+"  ")+m.text(e.Tone, e.Text))
	}
	lines = append(lines, m.text("muted", "q quit · Same Go controller in WASM and native. Preview only."))
	for i, s := range lines {
		lines[i] = "  " + ansi.Truncate(s, width, "…")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n") + "\n"
}

var _ tea.Model = (*Model)(nil)
