package portal

import "time"

// Estimate between inventory samples; browser clocks continue this at 1 Hz.
func (r HostRow) UptimeSeconds() float64 {
	seconds := r.Host.Uptime.Seconds()
	if r.Online && !r.SampledAt.IsZero() {
		seconds += time.Since(r.SampledAt).Seconds()
	}
	return seconds
}

// Revision sync is separate from reachability and from actual runtime health.
func (r HostRow) SyncLabel() string {
	if !r.Online {
		return "Unknown · offline"
	}
	if r.Temporary {
		return "Temporary installer"
	}
	if r.Unavailable {
		return "Status unavailable"
	}
	if r.Active != nil {
		if r.Active.State == "pending" {
			return "Deployment queued"
		}
		return "Deploying"
	}
	if r.Latest != nil && r.Latest.Status == "cancelled" {
		return "Apply cancelled"
	}
	if r.Latest != nil && r.Latest.Status == "failed" {
		return "Apply failed"
	}
	if r.RemoteSHA != "" && r.RemoteSHA != r.PulledSHA {
		return "Repository update"
	}
	if r.PulledSHA == "" {
		return "Not pulled"
	}
	if r.Latest == nil {
		return "Never applied"
	}
	if r.Latest.GitSHA != r.PulledSHA {
		return "Needs apply"
	}
	if r.Latest.Kind == "rollback" {
		return "Rolled back · review"
	}
	if r.Latest.Mode == "boot" {
		return "Staged for boot"
	}
	if r.Latest.Mode == "dry-activate" {
		return "Activation not applied"
	}
	if r.Latest.Mode == "" {
		return "Revision recorded"
	}
	return "Revision applied"
}
func (r HostRow) SyncClass() string {
	switch r.SyncLabel() {
	case "Deploying", "Deployment queued":
		return "deploying"
	case "Revision applied":
		return "online"
	case "Apply failed", "Apply cancelled":
		return "failed"
	case "Unknown · offline", "Revision recorded", "Status unavailable":
		return "offline"
	default:
		return "warn"
	}
}

func (r HostRow) SyncIcon() string {
	switch r.SyncClass() {
	case "online":
		return "✓"
	case "failed":
		return "!"
	case "offline":
		return "?"
	case "deploying":
		return ""
	default:
		return "↑"
	}
}

func (r HostRow) ApplyLabel() string {
	if r.Active != nil {
		return r.SyncLabel()
	}
	if r.SyncLabel() == "Revision applied" {
		return "Reapply"
	}
	if r.RemoteSHA != "" && r.RemoteSHA != r.PulledSHA {
		return "Pull update first"
	}
	if r.PulledSHA == "" {
		return "Pull revision first"
	}
	if r.SyncLabel() == "Apply failed" {
		return "Retry apply"
	}
	return "Apply update"
}

func (r HostRow) ApplyDisabled() bool {
	return r.Temporary || !r.Online || r.Unavailable || r.Active != nil || r.PulledSHA == "" || (r.RemoteSHA != "" && r.RemoteSHA != r.PulledSHA)
}
