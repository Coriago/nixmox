package views

import "nixmox/internal/ui/components/table"

// tables inspects the actual ordered block stream, not a parallel view projection.
func tables(v View) []table.Model {
	var result []table.Model
	for _, b := range v.Blocks {
		if b.Kind == Tables {
			result = append(result, b.Table)
		}
	}
	return result
}
