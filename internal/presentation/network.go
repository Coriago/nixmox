// Package presentation contains formatting shared by the browser, CLI, and TUI.
package presentation

import "nixmox/internal/domain"

func Network(n domain.NetworkStatus) string {
	if n.Freshness == "unknown" {
		return "Network status unavailable. Run Check network."
	}
	prefix := ""
	if n.Freshness == "stale" {
		prefix = "Stale observation: "
	}
	switch n.Network.Status {
	case "ready":
		if n.Network.Interface != nil && n.Network.Address != nil {
			return prefix + "Connected on " + *n.Network.Interface + " (" + *n.Network.Address + ")."
		}
	case "obtaining_address":
		return prefix + "Obtaining network address…"
	case "disconnected":
		return prefix + "Not connected to a network. Plug in a wired connection, or connect to Wi-Fi."
	}
	return prefix + "Network status unavailable."
}
func Task(t domain.Task) string { return t.ID + " · " + t.Kind + " · " + t.State + " · " + t.Phase }

type TaskAction struct {
	Type    string
	Label   string
	Enabled bool
	Reason  string
}

func TaskActions(t domain.Task) []TaskAction {
	cancel := TaskAction{Type: "task.cancel", Label: "Cancel", Enabled: t.Cancellation.Allowed && !t.Terminal()}
	if !cancel.Enabled {
		cancel.Reason = "Cancellation is unavailable in this state."
	}
	retry := TaskAction{Type: "task.retry", Label: "Retry", Enabled: t.Terminal()}
	if !retry.Enabled {
		retry.Reason = "Retry is available after the task finishes."
	}
	return []TaskAction{cancel, retry}
}
