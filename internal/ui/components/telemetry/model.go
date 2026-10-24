package telemetry

import "fmt"

type Visual string

const (
	History     Visual = "history"
	Waiting     Visual = "waiting"
	Capacity    Visual = "capacity"
	Unavailable Visual = "unavailable"
)

// Visualization decides component presence once, not independently in adapters.
func (m Model) Visualization() Visual {
	if !m.Known {
		return Unavailable
	}
	if len(m.History) == 1 {
		return Waiting
	}
	if len(m.History) > 0 {
		return History
	}
	return Capacity
}

// Caption describes the visual consistently, including unavailable and partial samples.
func (m Model) Caption() string {
	switch m.Visualization() {
	case Waiting:
		return "Waiting for samples"
	case Unavailable:
		return "No live observation"
	case Capacity:
		return fmt.Sprintf("%.0f%% used", m.Fraction*100)
	}
	return ""
}

type Model struct {
	Label, Value, Detail, Tone string
	Known                      bool
	Fraction                   float64
	History                    []float64
}
