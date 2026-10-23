package theme

import (
	"testing"
)

func TestNeutralOrangePalettes(t *testing.T) {
	for _, theme := range Themes {
		if theme.Accent != "#e57000" {
			t.Fatalf("%s lost the Proxmox-orange accent", theme.Name)
		}
	}
	tone := Themes[0]
	if tone.Background != "#181818" || tone.Surface != "#222222" || tone.Raised != "#303030" {
		t.Fatal("default surfaces must be neutral grey, not blue-tinted")
	}
}
