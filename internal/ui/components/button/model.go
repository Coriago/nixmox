package button

import "nixmox/internal/ui"

type Model struct {
	Label, Tone, Hint string
	Action            ui.Action
	Active, Disabled  bool
}
