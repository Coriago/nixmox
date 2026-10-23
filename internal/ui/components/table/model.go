package table

import (
	"nixmox/internal/ui"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/progress"
)

type Cell struct {
	Text, Detail, Tone string
	Badge              bool
	Bar                progress.Bar
}
type Record struct {
	ID, Marker, Tone string
	Controls         []button.Model
	Cells            []Cell
	Action           ui.Action
}
type Model struct {
	Title, Empty string
	Columns      []string
	Records      []Record
}
