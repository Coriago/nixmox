package browser

import (
	_ "embed"
	"fmt"
	"math"
	"strings"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui/components/progress"
	bars "nixmox/internal/ui/components/progress/browser"
	"nixmox/internal/ui/components/telemetry"
)

func metricChart(history []float64) app.UI {
	if len(history) < 2 {
		return app.Div().Class("chart-empty").Text("Waiting for samples")
	}
	var points strings.Builder
	for i, v := range history {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			v = 0
		}
		fmt.Fprintf(&points, "%.2f,%.2f ", float64(i)*300/float64(len(history)-1), 64-math.Max(0, math.Min(1, v))*60)
	}
	// Only bounded numeric coordinates enter Raw; labels are rendered as text.
	return app.Raw(fmt.Sprintf(`<svg class="metric-chart" viewBox="0 0 300 70" preserveAspectRatio="none" aria-hidden="true"><path d="M0 4H300 M0 34H300 M0 64H300" class="chart-grid"/><polygon points="0,70 %s300,70" class="chart-area"/><polyline points="%s" class="chart-line"/></svg>`, points.String(), points.String()))
}

func Render(items []telemetry.Model) app.UI {
	return app.Section().Class("telemetry").Aria("label", "Host telemetry").Body(
		app.Div().Class("section-heading").Body(app.H2().Text("Live telemetry"), app.Small().Text("MOCK SAMPLES · recent 60s · gaps are not zero")),
		app.Div().Class("metric-grid").Body(app.Range(items).Slice(func(i int) app.UI {
			m := items[i]
			return app.Section().Class("metric", "tone-"+m.Tone).Body(
				app.Div().Class("metric-heading").Body(app.H3().Text(m.Label), app.Small().Text("MOCK")),
				app.Strong().Class("metric-value").Text(m.Value),
				app.P().Class("metric-detail").Text(m.Detail),
				app.If(m.Visualization() != telemetry.Unavailable, func() app.UI {
					if m.Visualization() == telemetry.History {
						return metricChart(m.History)
					}
					return app.Div().Class("capacity").Body(bars.Render(progress.Bar{Value: m.Fraction, Display: m.Fraction, Known: true, Label: m.Detail}), app.Small().Textf("%.0f%% used", m.Fraction*100))
				}).Else(func() app.UI { return app.Div().Class("chart-empty").Text("No live observation") }),
			)
		})),
	)
}

//go:embed style.css
var CSS string
