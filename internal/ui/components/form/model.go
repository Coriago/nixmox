// Package form describes small editable forms shared by the browser and terminal.
package form

const InputLimit = 256 << 10

type Choice struct{ Value, Label string }
type Field struct {
	ID, Label, Kind, Value string
	Required               bool
	Choices                []Choice
}
type Model struct {
	ID, Title string
	Fields    []Field
	Note      string
	Disabled  bool
}
