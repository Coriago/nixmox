package progress

import (
	"fmt"
	"slices"
	"strings"
)

// View is the complete display contract. Neither renderer computes counts,
// progress, ordering, phases, elapsed time, logs, selection or control labels.
type View struct {
	Title, Subtitle, Status, Tone, Spinner, Elapsed string
	Overall                                         Bar
	Stats                                           []Stat
	Rows                                            []Row
	Feed                                            []FeedEntry
	Controls                                        []Control
	Selected                                        string
	ReducedMotion                                   bool
}
type Bar struct {
	Value, Display float64
	Known          bool
	Label          string
}
type Stat struct{ Label, Value, Detail, Tone string }
type Row struct {
	ID, Name, Kind, Host, State, Phase, Elapsed, Rate, Marker, Tone string
	Selected, Expanded                                              bool
	Progress                                                        Bar
	Detail                                                          []string
}
type Control struct {
	Action, Label, Key string
	Active             bool
}
type Token struct{ Name, Color string }

var palette = []Token{
	{"background", "#0b1117"}, {"surface", "#111c25"}, {"text", "#e2ebf3"},
	{"muted", "#91a5b7"}, {"line", "#2a3a47"}, {"accent", "#78e6c3"},
	{"blue", "#8ebaff"}, {"warning", "#e7c17b"}, {"danger", "#ff929f"},
}

func Palette() []Token { return slices.Clone(palette) }
func tone(s State) string {
	switch s {
	case Running:
		return "blue"
	case Succeeded:
		return "accent"
	case Failed:
		return "danger"
	}
	return "muted"
}
func (m *Model) View() View {
	v := View{Title: m.title, Subtitle: m.subtitle, Status: "RUNNING", Tone: "blue", Elapsed: duration(m.elapsed), Selected: m.selected, ReducedMotion: m.reduced}
	v.Spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[(m.frame/2)%10]
	if m.reduced {
		v.Spinner = "●"
	}
	done, builds, built, active, failed, paths, downloaded := 0, 0, 0, 0, 0, 0, 0
	var received, total, rate float64
	totalsKnown := true
	for _, a := range m.items {
		if terminal(a.State) {
			done++
		}
		if a.State == Failed {
			failed++
		}
		if a.State == Running {
			active++
		}
		if a.Kind == Build {
			builds++
			if a.State == Succeeded {
				built++
			}
		} else {
			paths++
			if a.State == Succeeded {
				downloaded++
			}
			received += float64(a.Done)
			if a.Known {
				total += float64(a.Total)
			} else {
				totalsKnown = false
			}
			if a.State == Running {
				rate += a.BytesPerSecond
			}
		}
	}
	if done == len(m.items) && done > 0 {
		v.Status = "COMPLETE"
		v.Tone = "accent"
		v.Spinner = "✓"
	}
	if failed > 0 {
		v.Status = "FAILED"
		v.Tone = "danger"
		v.Spinner = "!"
	}
	actual := 0.0
	if len(m.items) > 0 {
		actual = float64(done) / float64(len(m.items))
	}
	v.Overall = Bar{Value: actual, Display: m.fraction("overall", actual), Known: len(m.items) > 0, Label: fmt.Sprintf("%d / %d activities finished", done, len(m.items))}
	downloadValue := fmt.Sprintf("%.1f / %.1f MiB", received/(1<<20), total/(1<<20))
	if !totalsKnown {
		downloadValue = fmt.Sprintf("%.1f MiB / unknown", received/(1<<20))
	}
	v.Stats = []Stat{
		{"BUILDS", fmt.Sprintf("%d / %d", built, builds), fmt.Sprintf("%d failed · %d active activities", failed, active), "accent"},
		{"DOWNLOADS", downloadValue, fmt.Sprintf("%d / %d paths · %.1f MiB/s", downloaded, paths, rate/(1<<20)), "blue"},
		{"ELAPSED", v.Elapsed, "Observed time · no invented build ETA", "text"},
	}
	for _, a := range m.visible() {
		r := Row{ID: a.ID, Name: a.Name, Kind: string(a.Kind), Host: a.Host, State: string(a.State), Phase: a.Phase, Elapsed: duration(a.Elapsed), Tone: tone(a.State), Selected: a.ID == m.selected, Expanded: m.expanded[a.ID]}
		r.Marker = "·"
		switch a.State {
		case Running:
			r.Marker = v.Spinner
		case Succeeded:
			r.Marker = "✓"
		case Failed:
			r.Marker = "!"
		}
		// A failed sibling must not replace a still-running row's spinner with '!'.
		if a.State == Running {
			r.Marker = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[(m.frame/2)%10]
			if m.reduced {
				r.Marker = "●"
			}
		}
		r.Progress = Bar{Label: "No measured build percentage"}
		if a.Kind == Download {
			r.Rate = "—"
			if a.State == Running {
				r.Rate = fmt.Sprintf("%.1f MiB/s", a.BytesPerSecond/(1<<20))
			}
			if a.Known && a.Total > 0 {
				value := float64(a.Done) / float64(a.Total)
				r.Progress = Bar{Value: value, Display: m.fraction("row/"+a.ID, value), Known: true, Label: bytes(a.Done) + " / " + bytes(a.Total)}
			} else {
				r.Progress.Label = bytes(a.Done) + " / unknown"
			}
		}
		if r.Expanded {
			r.Detail = []string{string(a.Kind) + " on " + a.Host + " · " + a.Phase}
			if a.Kind == Download && a.Known {
				r.Detail = append(r.Detail, fmt.Sprintf("%d / %d bytes", a.Done, a.Total))
			}
			r.Detail = append(r.Detail, a.Logs...)
			if len(a.Logs) == 0 {
				r.Detail = append(r.Detail, "No output yet.")
			}
		}
		v.Rows = append(v.Rows, r)
	}
	// Newest first, bounded in the view as well as in retained state.
	for i := len(m.feed) - 1; i >= max(0, len(m.feed)-4); i-- {
		v.Feed = append(v.Feed, m.feed[i])
	}
	completedLabel := "Hide completed"
	if m.hideCompleted {
		completedLabel = "Show completed"
	}
	motionLabel := "Reduce motion"
	if m.reduced {
		motionLabel = "Animate"
	}
	v.Controls = []Control{{Completed, completedLabel, "c", m.hideCompleted}, {Motion, motionLabel, "m", m.reduced}}
	return v
}
func CSSVariables() string {
	var s strings.Builder
	for _, t := range palette {
		fmt.Fprintf(&s, "--%s:%s;", t.Name, t.Color)
	}
	return s.String()
}
