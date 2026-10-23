// Package theme defines the shared semantic palettes for both renderers.
package theme

import "fmt"

type Theme struct {
	Name, Background, Surface, Raised, Text, Muted, Border string
	Accent, Running, Success, Warning, Danger              string
}

var Themes = []Theme{
	{"Graphite", "#181818", "#222222", "#303030", "#eeeeee", "#aaaaaa", "#414141", "#e57000", "#e99843", "#79bf86", "#e8bd67", "#e55353"},
	{"Charcoal", "#1c1916", "#26221e", "#342e27", "#eee9e2", "#b3aaa0", "#494139", "#e57000", "#edaa63", "#86bc80", "#d9b264", "#e55353"},
	{"Paper", "#eeebe6", "#faf8f4", "#e2ded7", "#292724", "#69645e", "#cdc7bd", "#e57000", "#a94e00", "#38733c", "#94600e", "#b33131"},
}

func (t Theme) CSS() string {
	return fmt.Sprintf("--bg:%s;--surface:%s;--raised:%s;--text:%s;--muted:%s;--border:%s;--accent:%s;--running:%s;--success:%s;--warning:%s;--danger:%s;",
		t.Background, t.Surface, t.Raised, t.Text, t.Muted, t.Border, t.Accent, t.Running, t.Success, t.Warning, t.Danger)
}
func (t Theme) Color(tone string) string {
	switch tone {
	case "accent":
		return t.Accent
	case "running":
		return t.Running
	case "success":
		return t.Success
	case "warning":
		return t.Warning
	case "danger":
		return t.Danger
	case "muted":
		return t.Muted
	}
	return t.Text
}
