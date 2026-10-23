package telemetry

type Visual string

const (
	History     Visual = "history"
	Capacity    Visual = "capacity"
	Unavailable Visual = "unavailable"
)

// Visualization decides component presence once, not independently in adapters.
func (m Model) Visualization() Visual {
	if !m.Known {
		return Unavailable
	}
	if len(m.History) > 0 {
		return History
	}
	return Capacity
}

type Model struct {
	Label, Value, Detail, Tone string
	Known                      bool
	Fraction                   float64
	History                    []float64
}
