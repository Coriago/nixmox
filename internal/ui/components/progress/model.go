package progress

import (
	"math"
	"time"

	"github.com/charmbracelet/harmonica"
)

const FrameInterval = time.Second / 30

var progressSpring = harmonica.NewSpring(harmonica.FPS(30), 14, 1)

// Spring is presentation only. Value is observed; Display is eased and clamped.
// A critically damped spring cannot imply overshoot of measured completion.
type Spring struct{ Position, Velocity float64 }
type Bar struct {
	Value, Display float64
	Known          bool
	Label          string
}

func (s *Spring) Step(target float64, reduced bool) {
	target = math.Max(0, math.Min(1, target))
	if reduced {
		s.Position, s.Velocity = target, 0
		return
	}
	s.Position, s.Velocity = progressSpring.Update(s.Position, s.Velocity, target)
}
func (s Spring) Bar(value float64, known bool, label string) Bar {
	return Bar{Value: value, Display: math.Max(0, math.Min(1, s.Position)), Known: known, Label: label}
}
