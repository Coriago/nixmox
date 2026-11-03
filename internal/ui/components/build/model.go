// Package build projects Nix internal-json activity events for both interfaces.
// Protocol: Nix libutil logging.hh/logging.cc; unknown events remain harmless.
package build

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"nixmox/internal/ui"
	logs "nixmox/internal/ui/components/log"
)

const (
	activityDownload   = 101
	activityBuilds     = 104
	activityBuild      = 105
	activitySubstitute = 108
	resultBuildLog     = 101
	resultPhase        = 104
	resultProgress     = 105
	resultExpected     = 106
	maxActivities      = 256
)

type Activity struct {
	ID, Parent                      uint64
	Type                            int
	Name, Phase                     string
	Done, Expected, Running, Failed uint64
	Stopped                         bool
}

type Model struct {
	Activities []Activity
	Log        logs.Model
	Status     string
	Truncated  bool
}

type event struct {
	Action  string            `json:"action"`
	ID      uint64            `json:"id"`
	Parent  uint64            `json:"parent"`
	Type    int               `json:"type"`
	Text    string            `json:"text"`
	Message string            `json:"msg"`
	Fields  []json.RawMessage `json:"fields"`
}

func text(fields []json.RawMessage, index int) string {
	var value string
	if index < len(fields) {
		_ = json.Unmarshal(fields[index], &value)
	}
	return ui.Text(value, 500, ' ')
}
func number(fields []json.RawMessage, index int) uint64 {
	var value uint64
	if index < len(fields) {
		_ = json.Unmarshal(fields[index], &value)
	}
	return value
}

func (m *Model) ApplyLine(line string) {
	if !strings.HasPrefix(line, "@nix ") {
		m.Log.Append(line)
		return
	}
	var update event
	if json.Unmarshal([]byte(strings.TrimPrefix(line, "@nix ")), &update) != nil {
		m.Log.Append("Unrecognized Nix log event")
		return
	}
	switch update.Action {
	case "start":
		m.start(update)
	case "stop":
		if activity := m.activity(update.ID); activity != nil {
			activity.Stopped = true
		}
	case "result":
		m.result(update)
	case "msg":
		m.Log.Append(update.Message)
	}
}

func (m *Model) activity(id uint64) *Activity {
	return ui.Find(m.Activities, func(a Activity) bool { return a.ID == id })
}
func (m *Model) start(update event) {
	if m.activity(update.ID) != nil {
		return
	}
	if len(m.Activities) == maxActivities {
		index := slices.IndexFunc(m.Activities, func(a Activity) bool { return a.Stopped })
		if index < 0 {
			m.Truncated = true
			return
		}
		m.Activities = slices.Delete(m.Activities, index, index+1)
		m.Truncated = true
	}
	name := ui.Text(update.Text, 500, ' ')
	if name == "" {
		name = text(update.Fields, 0)
	}
	if update.Type == activityBuild {
		derivation := path.Base(text(update.Fields, 0))
		if len(derivation) > 33 && derivation[32] == '-' {
			name = strings.TrimSuffix(derivation[33:], ".drv")
		}
	}
	m.Activities = append(m.Activities, Activity{
		ID: update.ID, Parent: update.Parent, Type: update.Type, Name: name,
	})
}
func (m *Model) result(update event) {
	if update.Type == resultBuildLog {
		m.Log.Append(text(update.Fields, 0))
		return
	}
	activity := m.activity(update.ID)
	if activity == nil {
		return
	}
	switch update.Type {
	case resultPhase:
		activity.Phase = text(update.Fields, 0)
	case resultProgress:
		activity.Done = number(update.Fields, 0)
		activity.Expected = number(update.Fields, 1)
		activity.Running = number(update.Fields, 2)
		activity.Failed = number(update.Fields, 3)
	case resultExpected:
		if activity.Type == activityBuilds && number(update.Fields, 0) == activityBuild {
			activity.Expected = number(update.Fields, 1)
		}
	}
}

// Rows keeps build work visible instead of filling the console with completed
// cache probes. Activity state remains separate from the task's final outcome.
func (m Model) Rows() []Activity {
	rows := make([]Activity, 0, len(m.Activities))
	for _, activity := range m.Activities {
		if strings.Contains(activity.Name, ".narinfo") {
			continue
		}
		if activity.Kind() != "Activity" || (!activity.Stopped && activity.Name != "") {
			rows = append(rows, activity)
		}
	}
	slices.SortStableFunc(rows, func(a, b Activity) int {
		priority := func(a Activity) int {
			if a.Type == activityBuilds {
				return 0
			}
			if !a.Stopped {
				return 1
			}
			if a.Type == activityBuild {
				return 2
			}
			return 3
		}
		return priority(a) - priority(b)
	})
	return rows[:min(len(rows), 16)]
}

func (a Activity) State() string {
	if a.Stopped {
		if a.Phase != "" {
			return a.Phase + " · stopped"
		}
		return "Stopped"
	}
	return a.Phase
}

func (a Activity) Kind() string {
	switch a.Type {
	case activityBuild:
		return "Build"
	case activityDownload, activitySubstitute:
		return "Download"
	case activityBuilds:
		return "Builds"
	default:
		return "Activity"
	}
}
func (a Activity) Progress() string {
	if a.Expected == 0 {
		return "Total unknown"
	}
	if a.Type == activityDownload {
		return fmt.Sprintf("%s / %s", bytes(a.Done), bytes(a.Expected))
	}
	progress := fmt.Sprintf("%d / %d", a.Done, a.Expected)
	if a.Failed > 0 {
		progress += fmt.Sprintf(" · %d failed", a.Failed)
	}
	return progress
}
func bytes(value uint64) string {
	if value >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(value)/(1<<20))
	}
	if value >= 1<<10 {
		return fmt.Sprintf("%.1f KiB", float64(value)/(1<<10))
	}
	return fmt.Sprintf("%d B", value)
}
func (m Model) Summary() string {
	builds, downloads := 0, 0
	for _, a := range m.Activities {
		if a.Stopped {
			continue
		}
		if a.Type == activityBuild {
			builds++
		}
		if a.Type == activityDownload || a.Type == activitySubstitute {
			downloads++
		}
	}
	if m.Status != "" && m.Status != "running" && m.Status != "queued" {
		return "Task " + m.Status + " · activity stops alone do not imply successful builds"
	}
	return fmt.Sprintf("%d active builds · %d active downloads", builds, downloads)
}
