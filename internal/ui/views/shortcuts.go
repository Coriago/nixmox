package views

// Shortcut returns the same presentation action in the DOM and terminal.
func Shortcut(key string) (Action, bool) {
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		i := int(key[0] - '1')
		if i < len(Pages) {
			return Action{Kind: "page", ID: Pages[i].Name}, true
		}
	}
	bindings := map[string]string{"t": "theme", "m": "motion", "p": "pause", "r": "replay", "?": "help", "Escape": "back", "esc": "back"}
	kind, ok := bindings[key]
	return Action{Kind: kind}, ok
}
