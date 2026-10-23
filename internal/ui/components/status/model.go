package status

import (
	"nixmox/internal/domain"
	"nixmox/internal/ui"
)

func MembershipTone(state string) string {
	switch domain.Membership(state) {
	case domain.Pending:
		return "warning"
	case domain.Full:
		return "success"
	case domain.Removed:
		return "danger"
	}
	return "muted"
}

func Marker(state string, frame uint64, reduced bool) (string, string) {
	if ui.Active(state) {
		if reduced {
			return "●", "running"
		}
		return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[(frame/3)%10], "running"
	}
	switch state {
	case string(domain.Online):
		return "●", "success"
	case "succeeded":
		return "✓", "success"
	case "offline", "unknown":
		return "○", "muted"
	case "blocked", "stale":
		return "◌", "warning"
	case "failed":
		return "×", "danger"
	case "cancelled":
		return "−", "muted"
	}
	return "·", "muted"
}
