package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"nixmox/internal/client"
	"nixmox/internal/presentation"
)

type telemetryMessage struct {
	host       string
	generation uint64
	update     client.TelemetryUpdate
}
type telemetryPoint struct {
	at    time.Time
	value *float64
}

func (m Model) waitTelemetry() tea.Cmd {
	ch, host, generation := m.telemetryEvents, m.telemetry.HostID, m.telemetryGeneration
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return nil
		case update, ok := <-ch:
			if !ok {
				return nil
			}
			return telemetryMessage{host: host, generation: generation, update: update}
		}
	}
}
func (m Model) telemetryContent() string {
	var graph strings.Builder
	points := m.telemetryHistory
	if len(points) > m.viewport.Width {
		points = points[len(points)-m.viewport.Width:]
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	for _, point := range points {
		if point.value == nil {
			graph.WriteRune(' ')
		} else {
			graph.WriteRune(levels[min(7, max(0, int(*point.value*8)))])
		}
	}
	return "Host telemetry\n" + presentation.TelemetryText(m.telemetry) + "\nRecent CPU usage\n" + graph.String() + "\nHistory is retained for up to five minutes while viewed."
}
