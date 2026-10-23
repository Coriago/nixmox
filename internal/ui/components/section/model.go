package section

import "nixmox/internal/ui/components/button"

type Field struct{ Label, Value, Tone string }
type Model struct {
	Title, Note string
	Fields      []Field
	Controls    []button.Model
}
