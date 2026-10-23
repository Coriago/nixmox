package progress

import (
	"math"
	"testing"
)

func TestSpringAndReducedMotion(t *testing.T) {
	var s Spring
	previous := 0.0
	for range 180 {
		s.Step(.7, false)
		if math.IsNaN(s.Position) || s.Position < previous || s.Position > .70001 {
			t.Fatalf("invalid spring %+v", s)
		}
		previous = s.Position
	}
	if math.Abs(s.Position-.7) > .001 {
		t.Fatal("spring did not settle")
	}
	s.Step(.2, true)
	if s.Position != .2 || s.Velocity != 0 {
		t.Fatal("reduced motion must snap")
	}
	if b := s.Bar(.2, true, "2 / 10"); b.Value != .2 || b.Display != .2 || !b.Known {
		t.Fatal(b)
	}
}
