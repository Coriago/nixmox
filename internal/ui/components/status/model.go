// Package status maps observed states to presentation, without inventing health.
package status

import "nixmox/internal/ui"

func Marker(state string, frame uint64, reduced bool) (string, string) {
	if ui.Active(state) {
		if reduced {
			return "◐", "accent"
		}
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		return frames[(frame/4)%uint64(len(frames))], "accent"
	}
	switch state {
	case "succeeded", "online", "healthy":
		return "●", "success"
	case "failed", "unhealthy":
		return "×", "danger"
	case "manual-review", "unknown", "disconnected", "stale":
		return "!", "warning"
	case "cancelled", "removed":
		return "–", "muted"
	default:
		return "○", "muted"
	}
}

func MembershipTone(state string) string {
	switch state {
	case "full-stable", "full":
		return "success"
	case "pending":
		return "warning"
	default:
		return "muted"
	}
}
