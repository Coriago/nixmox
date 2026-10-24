package log

import (
	"fmt"

	"nixmox/internal/ui"
	"nixmox/internal/ui/components/button"
)

const MaxLogLines = 160

func FollowLabel(follow bool) string {
	if follow {
		return "Following live output"
	}
	return "Follow paused · Jump to latest"
}

// FollowState is shared interaction state; adapters only measure scroll position.
type FollowState struct{ Follow bool }

// Reaching the bottom does not silently resume a paused stream.
func (s *FollowState) Scrolled(atBottom bool) {
	if !atBottom {
		s.Follow = false
	}
}
func (s FollowState) Control() button.Model {
	return button.Model{Label: FollowLabel(s.Follow), Action: ui.Action{Kind: "follow"}, Active: s.Follow}
}

type Model struct {
	Lines []string
	Total uint64
}

func (l Model) Summary() string {
	return fmt.Sprintf("Retained %d / %d lines · limit %d", len(l.Lines), l.Total, MaxLogLines)
}

func (l *Model) Append(line string) {
	// Logs are plain text in both adapters, never terminal escapes or markup.
	line = ui.Text(line, 500, ' ')
	l.Lines = append(l.Lines, line)
	l.Total++
	if len(l.Lines) > MaxLogLines {
		l.Lines = append([]string(nil), l.Lines[len(l.Lines)-MaxLogLines:]...)
	}
}
