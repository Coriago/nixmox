package confirmation

type Model struct{ Title, Body, Target, Confirm, Action, ResourceID, Fingerprint string }

func (m Model) Tone() string {
	switch m.Action {
	case "shutdown", "cancel", "reject":
		return "danger"
	case "approve", "add":
		return "success"
	}
	return "primary"
}
