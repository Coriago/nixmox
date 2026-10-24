package views

import (
	"nixmox/internal/ui"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/form"
	logs "nixmox/internal/ui/components/log"
	"nixmox/internal/ui/components/section"
	"nixmox/internal/ui/components/table"
	"nixmox/internal/ui/components/telemetry"
)

type BlockKind string

const (
	Forms    BlockKind = "forms"
	Heading  BlockKind = "heading"
	Identity BlockKind = "identity"
	Tabs     BlockKind = "tabs"
	Metrics  BlockKind = "metrics"
	Search   BlockKind = "search"
	Controls BlockKind = "controls"
	Sections BlockKind = "sections"
	Tables   BlockKind = "tables"
	Preview  BlockKind = "preview"
	Logs     BlockKind = "logs"
	Notice   BlockKind = "notice"
)

// Block carries its data and position. There is no second layout-construction pass.
type Block struct {
	Kind    BlockKind
	Form    *form.Model
	Fields  []section.Field
	Buttons []button.Model
	Metrics []telemetry.Model
	Table   table.Model
	Section section.Model
	Lines   []string
	Log     *logs.Model
}

func (v *View) Add(blocks ...Block) { v.Blocks = append(v.Blocks, blocks...) }
func (v View) Find(kind BlockKind) *Block {
	return ui.Find(v.Blocks, func(b Block) bool { return b.Kind == kind })
}
