package log

import "nixmox/internal/ui"

const MaxLogLines = 160

func FollowLabel(follow bool) string {
	if follow {
		return "Following live output"
	}
	return "Follow paused · Jump to latest"
}

type Model struct {
	Lines []string
	Total uint64
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
