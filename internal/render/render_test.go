package render

import (
	"strings"
	"testing"
)

func TestTypedFieldsEscapingAndSafePaths(t *testing.T) {
	tplt := Template{SchemaVersion: 1, ID: "server", Kind: "configuration", Fields: []Field{{Name: "Name", Type: "hostname", Required: true}, {Name: "Value", Type: "text", Required: true}}, Outputs: []Output{{Path: "hosts/{{ .Name }}.nix", Template: "host"}}, Configuration: Configuration{Name: "{{ .Name }}", FlakeDirectory: ".", FlakeAttribute: "nixosConfigurations.{{ .Name }}"}}
	result, err := Render(tplt, map[string]any{"Name": "cedar", "Value": "${builtins.readFile /secret}\n\""}, map[string]string{"host": "{ value = {{ nix .Value }}; }"})
	if err != nil || !strings.Contains(result.Files[0].Content, `\${builtins.readFile /secret}\n\"`) {
		t.Fatal(result, err)
	}
	for _, name := range []string{"../secret", "/absolute", "UPPER", "a/b"} {
		if _, err := Render(tplt, map[string]any{"Name": name, "Value": "ok"}, map[string]string{"host": ""}); err == nil {
			t.Fatal("unsafe hostname", name)
		}
	}
	if _, err := Render(tplt, map[string]any{"Name": "cedar", "Value": true}, map[string]string{"host": ""}); err == nil {
		t.Fatal("wrong field type")
	}
	tplt.Outputs[0].Path = "../escape"
	if _, err := Render(tplt, map[string]any{"Name": "cedar", "Value": "ok"}, map[string]string{"host": ""}); err == nil {
		t.Fatal("path escaped")
	}
}
