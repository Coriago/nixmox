package presentation

import (
	"bytes"
	"encoding/json"

	"nixmox/internal/domain"
)

func TaskResult(t domain.Task) string {
	if len(t.Result) == 0 || string(t.Result) == "null" {
		return ""
	}
	var b bytes.Buffer
	if json.Indent(&b, t.Result, "", "  ") != nil {
		return "Result unavailable."
	}
	return b.String()
}
