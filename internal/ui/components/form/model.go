// Package form describes small editable forms shared by the browser and terminal.
package form

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
}
