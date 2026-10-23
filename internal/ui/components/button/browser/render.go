package browser

import (
	_ "embed"

	"github.com/maxence-charriere/go-app/v11/pkg/app"

	actions "nixmox/internal/ui/browser"
	"nixmox/internal/ui/components/button"
)

func Render(b button.Model, dispatch actions.Dispatch) app.UI {
	return app.Button().Type("button").Class("button", "button-"+b.Tone).Aria("pressed", b.Active).Disabled(b.Disabled).Title(b.Hint).
		OnClick(actions.Click(dispatch, b.Action), app.EventScope(b.Action.Kind, b.Action.ID)).Text(b.Label)
}

//go:embed style.css
var CSS string
