// Package render is the pure Go form/template engine shared by native and WASM clients.
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"nixmox/internal/domain"
)

type Field struct {
	Name     string   `json:"name"`
	Label    string   `json:"label,omitempty"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Choices  []string `json:"choices,omitempty"`
	Default  any      `json:"default,omitempty"`
}
type Output struct {
	Path     string `json:"path"`
	Template string `json:"template"`
	Format   string `json:"format,omitempty"`
}
type Configuration struct {
	Name           string `json:"name"`
	FlakeDirectory string `json:"flake_directory"`
	FlakeAttribute string `json:"flake_attribute"`
}
type Template struct {
	SchemaVersion        int                 `json:"schema_version"`
	ID                   string              `json:"id"`
	Kind                 string              `json:"kind"`
	Label                string              `json:"label"`
	Fields               []Field             `json:"fields"`
	Outputs              []Output            `json:"outputs"`
	Configuration        Configuration       `json:"configuration"`
	PreparationTemplates map[string][]string `json:"preparation_templates,omitempty"`
	Collection           json.RawMessage     `json:"collection,omitempty"`
	Mode                 string              `json:"mode,omitempty"`
	Source               *struct {
		Repository string `json:"repository"`
		Revision   string `json:"revision"`
		Template   string `json:"template"`
	} `json:"source,omitempty"`
	Assets []struct {
		SourcePath string `json:"source_path"`
		Path       string `json:"path"`
	} `json:"assets,omitempty"`
}
type File struct {
	Path    string `json:"path"`
	Format  string `json:"format"`
	Content string `json:"content"`
}
type Result struct {
	Values        map[string]any `json:"values"`
	Files         []File         `json:"files"`
	Configuration Configuration  `json:"configuration"`
}

var fieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
var hostname = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func SafePath(p string) bool {
	return p != "" && p != "." && path.Clean(p) == p && !path.IsAbs(p) && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00\r\n") && p != ".git" && !strings.HasPrefix(p, ".git/")
}
func invalid(message string) error { return domain.Err("INVALID_TEMPLATE", message, 422) }
func (t Template) Validate() error {
	if t.SchemaVersion != 1 || !domain.SafeID.MatchString(t.ID) || (t.Kind != "configuration" && t.Kind != "hardware" && t.Kind != "disk") || len(t.Fields) > 128 || len(t.Outputs) < 1 || len(t.Outputs) > 128 {
		return invalid("Invalid template schema, kind, fields or outputs.")
	}
	names := map[string]bool{}
	for _, f := range t.Fields {
		if !fieldName.MatchString(f.Name) || f.Name == "Hardware" || names[f.Name] {
			return invalid("Invalid, duplicate or reserved field: " + f.Name)
		}
		names[f.Name] = true
		switch f.Type {
		case "text", "hostname", "boolean", "integer", "select", "multiselect":
		case "disk", "partition":
			if t.Kind == "configuration" {
				return invalid("Device selectors belong to target preparation.")
			}
		default:
			return invalid("Unsupported field type: " + f.Name)
		}
		choices := map[string]bool{}
		for _, v := range f.Choices {
			if choices[v] || len(v) > 4096 {
				return invalid("Invalid choices: " + f.Name)
			}
			choices[v] = true
		}
		if (f.Type == "select" || f.Type == "multiselect") && len(choices) == 0 {
			return invalid("Selection field requires choices: " + f.Name)
		}
	}
	for _, o := range t.Outputs {
		if !SafePath(o.Template) || (o.Format != "" && o.Format != "nix" && o.Format != "json" && o.Format != "text") {
			return invalid("Invalid output template or format.")
		}
	}
	return nil
}
func (t Template) Values(input map[string]any) (map[string]any, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	values := map[string]any{}
	known := map[string]bool{}
	for _, f := range t.Fields {
		known[f.Name] = true
		v, exists := input[f.Name]
		if !exists {
			v = f.Default
		}
		if v == nil {
			if f.Required {
				return nil, domain.Err("FIELD_REQUIRED", "Required field: "+f.Name, 422)
			}
			switch f.Type {
			case "text", "hostname", "select":
				v = ""
			case "boolean":
				v = false
			case "integer":
				v = int64(0)
			case "multiselect":
				v = []string{}
			default:
				continue
			}
		}
		valid := true
		switch f.Type {
		case "text", "hostname", "select":
			s, ok := v.(string)
			valid = ok && len(s) <= 4096 && (!f.Required || s != "")
			if valid && f.Type == "hostname" && s != "" {
				valid = hostname.MatchString(s)
			}
			if valid && f.Type == "select" && s != "" {
				valid = contains(f.Choices, s)
			}
		case "boolean":
			_, valid = v.(bool)
		case "integer":
			switch n := v.(type) {
			case json.Number:
				i, e := n.Int64()
				valid = e == nil
				v = i
			case float64:
				valid = n >= -9007199254740991 && n <= 9007199254740991 && n == float64(int64(n))
				v = int64(n)
			case int:
				v = int64(n)
			case int64:
			default:
				valid = false
			}
		case "multiselect":
			list := []string{}
			switch x := v.(type) {
			case []string:
				list = x
			case []any:
				for _, item := range x {
					s, ok := item.(string)
					if !ok {
						valid = false
						break
					}
					list = append(list, s)
				}
			default:
				valid = false
			}
			seen := map[string]bool{}
			for _, s := range list {
				if seen[s] || !contains(f.Choices, s) {
					valid = false
				}
				seen[s] = true
			}
			valid = valid && (!f.Required || len(list) > 0)
			v = list
		case "disk", "partition":
			return nil, domain.Err("PREPARATION_REQUIRED", "Device values must be resolved from target observations.", 422)
		}
		if !valid {
			return nil, domain.Err("FIELD_INVALID", "Invalid field value: "+f.Name, 422)
		}
		values[f.Name] = v
	}
	for k := range input {
		if !known[k] {
			return nil, domain.Err("FIELD_INVALID", "Unknown field: "+k, 422)
		}
	}
	return values, nil
}
func contains(xs []string, s string) bool {
	for _, v := range xs {
		if s == v {
			return true
		}
	}
	return false
}
func Nix(v any) (string, error) {
	switch x := v.(type) {
	case string:
		r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "${", "\\${", "\n", "\\n", "\r", "\\r", "\t", "\\t")
		if strings.ContainsRune(x, 0) {
			return "", invalid("Nix strings cannot contain NUL.")
		}
		return "\"" + r.Replace(x) + "\"", nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(i, 10), nil
	case []string:
		parts := []string{}
		for _, s := range x {
			p, err := Nix(s)
			if err != nil {
				return "", err
			}
			parts = append(parts, p)
		}
		return "[ " + strings.Join(parts, " ") + " ]", nil
	case []any:
		parts := []string{}
		for _, v := range x {
			p, err := Nix(v)
			if err != nil {
				return "", err
			}
			parts = append(parts, p)
		}
		return "[ " + strings.Join(parts, " ") + " ]", nil
	default:
		return "", invalid("Unsupported Nix serializer value.")
	}
}

type bounded struct {
	bytes.Buffer
	max int
}

func (b *bounded) Write(p []byte) (int, error) {
	if len(p) > b.max-b.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}
func expand(source string, values map[string]any) (string, error) {
	if len(source) > 1<<20 {
		return "", invalid("Template exceeds its size bound.")
	}
	t, err := template.New("output").Option("missingkey=error").Funcs(template.FuncMap{"nix": Nix, "json": func(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }}).Parse(source)
	if err != nil {
		return "", invalid("Template syntax is invalid.")
	}
	out := &bounded{max: 1 << 20}
	if err = t.Execute(out, values); err != nil {
		return "", invalid("Template execution failed or exceeded its output bound.")
	}
	return out.String(), nil
}
func Render(t Template, input map[string]any, templates map[string]string) (Result, error) {
	values, err := t.Values(input)
	if err != nil {
		return Result{}, err
	}
	result := Result{Values: values, Files: []File{}}
	seen := map[string]bool{}
	total := 0
	for _, o := range t.Outputs {
		name, err := expand(o.Path, values)
		if err != nil {
			return result, err
		}
		if !SafePath(name) || seen[name] {
			return result, invalid("Unsafe or duplicate output path: " + name)
		}
		seen[name] = true
		text, ok := templates[o.Template]
		if !ok {
			return result, invalid("Missing pinned template: " + o.Template)
		}
		content, err := expand(text, values)
		if err != nil {
			return result, err
		}
		total += len(content)
		if total > 1<<20 {
			return result, invalid("Generated files exceed 1 MiB.")
		}
		format := o.Format
		if format == "" {
			format = "nix"
		}
		if format == "json" && !json.Valid([]byte(content)) {
			return result, invalid("Invalid JSON output: " + name)
		}
		result.Files = append(result.Files, File{Path: name, Format: format, Content: content})
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	result.Configuration.Name, err = expand(t.Configuration.Name, values)
	if err != nil {
		return result, err
	}
	result.Configuration.FlakeDirectory, err = expand(t.Configuration.FlakeDirectory, values)
	if err != nil {
		return result, err
	}
	result.Configuration.FlakeAttribute, err = expand(t.Configuration.FlakeAttribute, values)
	if err != nil {
		return result, err
	}
	if t.Kind == "configuration" && (!fieldName.MatchString(strings.ReplaceAll(result.Configuration.Name, "-", "_")) || (result.Configuration.FlakeDirectory != "." && !SafePath(result.Configuration.FlakeDirectory)) || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)*$`).MatchString(result.Configuration.FlakeAttribute)) {
		return result, invalid(fmt.Sprintf("Invalid configuration descriptor for %q.", result.Configuration.Name))
	}
	return result, nil
}
