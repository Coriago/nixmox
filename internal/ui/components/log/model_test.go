package log

import (
	"strings"
	"testing"
)

func TestLogsAreBoundedPlainText(t *testing.T) {
	var l Model
	for range 500 {
		l.Append("\x1b[31muntrusted\n\t\u202e output")
	}
	if len(l.Lines) != MaxLogLines || l.Total != 500 {
		t.Fatal("unbounded log")
	}
	for _, line := range l.Lines {
		if strings.ContainsAny(line, "\x1b\n\t\u202e") {
			t.Fatal("control sequence survived")
		}
	}
}
