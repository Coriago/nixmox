package confirmation

// Accent is supplied by the caller; dialogs do not interpret application actions.
type Model struct{ Title, Body, Target, Confirm, Accent string }

func (m Model) Tone() string {
	if m.Accent != "" {
		return m.Accent
	}
	return "primary"
}
