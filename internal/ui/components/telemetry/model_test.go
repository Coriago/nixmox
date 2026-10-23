package telemetry

import "testing"

func TestVisualizationIsRendererIndependent(t *testing.T) {
	for _, test := range []struct {
		model  Model
		visual Visual
	}{
		{Model{Known: true, History: []float64{.1, .3}}, History},
		{Model{Known: true, Fraction: .3}, Capacity},
		{Model{Known: false, History: []float64{.1, .3}}, Unavailable},
	} {
		if test.model.Visualization() != test.visual {
			t.Fatal("visual element selection must happen in shared code")
		}
	}
}
