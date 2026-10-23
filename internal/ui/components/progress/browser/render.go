package browser

import (
	_ "embed"
	"fmt"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui/components/progress"
)

func Render(b progress.Bar) app.UI {
	return app.Div().Class("progress").Role("progressbar").Aria("label", b.Label).
		Aria("valuemin", 0).Aria("valuemax", 100).Aria("valuenow", fmt.Sprintf("%.0f", b.Value*100)).
		Body(app.Div().Class("progress-fill").Style("width", fmt.Sprintf("%.3f%%", b.Display*100)))
}

//go:embed style.css
var CSS string
