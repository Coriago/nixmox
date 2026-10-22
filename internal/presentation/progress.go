package presentation

import (
	"encoding/json"
	"fmt"
	"nixmox/internal/domain"
	"strings"
	"time"
)

// Decode only presentation fields; transports do not depend on the Nix adapter.
func TaskProgress(task domain.Task) string {
	if task.Progress == nil {
		return ""
	}
	b, _ := json.Marshal(task.Progress)
	var p struct {
		Phase          string    `json:"phase"`
		Freshness      string    `json:"freshness"`
		ObservedAt     time.Time `json:"observed_at"`
		Availability   string    `json:"availability"`
		Activation     string    `json:"activation"`
		Observation    string    `json:"observation"`
		ExpectedOutput string    `json:"expected_output"`
		RunningOutput  string    `json:"running_output"`
		Downloads      struct {
			BytesReceived  uint64   `json:"bytes_received"`
			BytesTotal     *uint64  `json:"bytes_total"`
			BytesPerSecond *float64 `json:"bytes_per_second"`
			Active         uint64   `json:"active_paths"`
		} `json:"downloads"`
		Builds struct {
			Completed uint64  `json:"completed_derivations"`
			Total     *uint64 `json:"total_derivations"`
			Active    uint64  `json:"active_derivations"`
			Failed    uint64  `json:"failed_derivations"`
		} `json:"builds"`
	}
	if json.Unmarshal(b, &p) != nil {
		return "Progress unavailable."
	}
	if p.Availability == "unavailable" {
		return "Structured Nix progress has not been reported yet."
	}
	if p.Observation != "" {
		return fmt.Sprintf("%s · %s\nActivation: %s · observation: %s\nExpected: %s\nRunning: %s", p.Phase, p.Freshness, p.Activation, p.Observation, p.ExpectedOutput, p.RunningOutput)
	}
	stale := !task.Terminal() && time.Since(p.ObservedAt) > 5*time.Second
	freshness := p.Freshness
	if stale {
		freshness = "stale"
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s · %s\nDownloads: %s", p.Phase, freshness, formatBytes(p.Downloads.BytesReceived))
	if p.Downloads.BytesTotal != nil {
		fmt.Fprintf(&text, " / %s known", formatBytes(*p.Downloads.BytesTotal))
	} else {
		text.WriteString(" · total unknown")
	}
	if p.Downloads.BytesPerSecond != nil && !stale {
		fmt.Fprintf(&text, " · %s/s", formatBytes(uint64(*p.Downloads.BytesPerSecond)))
	}
	fmt.Fprintf(&text, "\nBuilds: %d", p.Builds.Completed)
	if p.Builds.Total != nil {
		fmt.Fprintf(&text, " / %d known", *p.Builds.Total)
	} else {
		text.WriteString(" · total unknown")
	}
	fmt.Fprintf(&text, " · %d active · %d failed", p.Builds.Active, p.Builds.Failed)
	return text.String()
}
