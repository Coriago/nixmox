package status

import (
	"testing"
)

func TestStatusSemantics(t *testing.T) {
	a, _ := Marker("running", 0, false)
	b, _ := Marker("running", 12, false)
	if a == b {
		t.Fatal("spinner is not animated")
	}
	a, _ = Marker("running", 0, true)
	b, _ = Marker("running", 12, true)
	if a != b {
		t.Fatal("reduced spinner animated")
	}
}
