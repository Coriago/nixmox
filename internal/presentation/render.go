package presentation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode/utf8"

	"nixmox/internal/domain"
)

type TemplateRenderer struct{}

var hostname = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func NixValue(v any) (string, error) {
	switch value := v.(type) {
	case string:
		if !utf8.ValidString(value) {
			return "", errors.New("invalid UTF-8")
		}
		for _, c := range value {
			if c < 32 && c != '\n' && c != '\r' && c != '\t' {
				return "", errors.New("unsupported control character")
			}
		}
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "${", `\${`).Replace(value) + `"`, nil
	case bool:
		return strconv.FormatBool(value), nil
	case json.Number:
		if _, err := value.Int64(); err != nil {
			return "", errors.New("Nix value must be an integer")
		}
		return string(value), nil
	case int:
		return strconv.Itoa(value), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case []string:
		items := make([]any, len(value))
		for i, s := range value {
			items[i] = s
		}
		return NixValue(items)
	case []any:
		if len(value) > 256 {
			return "", errors.New("list exceeds limit")
		}
		parts := make([]string, len(value))
		for i, item := range value {
			serialized, err := NixValue(item)
			if err != nil {
				return "", err
			}
			parts[i] = serialized
		}
		return "[ " + strings.Join(parts, " ") + " ]", nil
	default:
		return "", fmt.Errorf("unsupported Nix value %T", v)
	}
}
func SafePath(value string) error {
	if value == "" || value == "." || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, "\\\x00") {
		return errors.New("output path must be a clean relative path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == ".git" {
			return errors.New("output escapes repository or enters Git metadata")
		}
	}
	return nil
}
func valuesWithDefaults(schema domain.Template, values domain.Values) (domain.Values, error) {
	result := domain.Values{}
	for _, field := range schema.Fields {
		if len(field.Default) > 0 {
			var value any
			d := json.NewDecoder(bytes.NewReader(field.Default))
			d.UseNumber()
			if err := d.Decode(&value); err != nil {
				return nil, err
			}
			result[field.Name] = value
		}
	}
	for key, value := range values {
		result[key] = value
	}
	return result, nil
}
func (TemplateRenderer) Validate(schema domain.Template, values domain.Values) []domain.FieldError {
	issues := []domain.FieldError{}
	add := func(name, code, message string) {
		issues = append(issues, domain.FieldError{Field: name, Code: code, Message: message})
	}
	if schema.SchemaVersion != 1 || len(schema.Fields) > 128 || len(schema.Outputs) > 64 {
		add("template", "INVALID_TEMPLATE", "Unsupported schema or oversized template")
		return issues
	}
	values, err := valuesWithDefaults(schema, values)
	if err != nil {
		add("template", "INVALID_DEFAULT", err.Error())
		return issues
	}
	fields := map[string]bool{}
	selected := map[string]bool{}
	for _, field := range schema.Fields {
		if field.Name == "" || field.Name == "Hardware" || fields[field.Name] {
			add(field.Name, "INVALID_FIELD", "Duplicate or reserved field")
		}
		fields[field.Name] = true
		value, exists := values[field.Name]
		if !exists {
			if field.Required {
				add(field.Name, "REQUIRED", "A value is required")
			}
			continue
		}
		valid := false
		switch field.Type {
		case "text", "hostname", "select", "disk", "partition":
			s, ok := value.(string)
			valid = ok && utf8.ValidString(s) && len(s) <= 65536
			if field.Required && s == "" {
				valid = false
			}
			if field.Type == "hostname" {
				valid = valid && hostname.MatchString(s)
			}
			if field.Type == "select" {
				valid = valid && contains(field.Choices, s)
			}
			if (field.Type == "disk" || field.Type == "partition") && ok && s != "" {
				if selected[s] {
					valid = false
				}
				selected[s] = true
			}
		case "boolean":
			_, valid = value.(bool)
		case "integer":
			switch v := value.(type) {
			case int, int64:
				valid = true
			case json.Number:
				_, err := v.Int64()
				valid = err == nil
			}
		case "multiselect":
			items, ok := stringList(value)
			valid = ok && len(items) <= 256 && (!field.Required || len(items) > 0)
			seen := map[string]bool{}
			for _, item := range items {
				if seen[item] || !contains(field.Choices, item) {
					valid = false
				}
				seen[item] = true
			}
		}
		if !valid {
			add(field.Name, "INVALID_VALUE", "Value does not match the declared field type or choices")
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !fields[key] {
			add(key, "UNKNOWN_FIELD", "Field is not declared by this template")
		}
	}
	return issues
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}
func stringList(value any) ([]string, bool) {
	switch list := value.(type) {
	case []string:
		return list, true
	case []any:
		result := make([]string, len(list))
		for i, v := range list {
			s, ok := v.(string)
			if !ok {
				return nil, false
			}
			result[i] = s
		}
		return result, true
	}
	return nil, false
}

type limitedText struct{ bytes.Buffer }

func (w *limitedText) Write(p []byte) (int, error) {
	if len(p) > domain.CommandPayloadLimit-w.Len() {
		return 0, errors.New("rendered output exceeds size limit")
	}
	return w.Buffer.Write(p)
}
func render(name, text string, values domain.Values) (string, error) {
	if len(text) > domain.CommandPayloadLimit {
		return "", errors.New("template exceeds size limit")
	}
	t, err := template.New(name).Option("missingkey=error").Funcs(template.FuncMap{"nix": NixValue, "json": func(v any) (string, error) { data, e := json.Marshal(v); return string(data), e }}).Parse(text)
	if err != nil {
		return "", err
	}
	out := &limitedText{}
	if err = t.Execute(out, values); err != nil {
		return "", err
	}
	return out.String(), nil
}
func (r TemplateRenderer) Preview(request PreviewRequest) PreviewResult {
	result := PreviewResult{Revision: request.Revision, Errors: r.Validate(request.Template, request.Values), Files: []domain.DraftBuffer{}}
	if len(result.Errors) > 0 {
		return result
	}
	values, _ := valuesWithDefaults(request.Template, request.Values)
	old := map[string]domain.DraftBuffer{}
	for _, file := range request.Files {
		old[file.Path] = file
	}
	seen := map[string]bool{}
	total := 0
	for _, output := range request.Template.Outputs {
		fail := func(err error) {
			result.Errors = append(result.Errors, domain.FieldError{Field: output.Path, Code: "RENDER_FAILED", Message: err.Error()})
		}
		name, err := render("path", output.Path, values)
		if err != nil {
			fail(err)
			continue
		}
		if err = SafePath(name); err != nil {
			fail(err)
			continue
		}
		if seen[name] {
			fail(errors.New("duplicate output path"))
			continue
		}
		seen[name] = true
		if err = SafePath(output.Template); err != nil {
			fail(err)
			continue
		}
		source, ok := request.Templates[output.Template]
		if !ok {
			fail(errors.New("pinned template file unavailable"))
			continue
		}
		content, err := render(name, source, values)
		if err != nil {
			fail(err)
			continue
		}
		total += len(content)
		if total > 1<<20 {
			fail(errors.New("preview exceeds total size limit"))
			break
		}
		if output.Format == "json" && !json.Valid([]byte(content)) {
			fail(errors.New("invalid generated JSON"))
			continue
		}
		if !contains([]string{"", "nix", "json", "text"}, output.Format) {
			fail(errors.New("unsupported output format"))
			continue
		}
		file := domain.DraftBuffer{Path: name, GeneratedBaseline: content, Content: content}
		if prior, ok := old[name]; ok && prior.ManuallyEdited {
			file = prior
			if content != prior.GeneratedBaseline {
				file.Regenerated = &content
			} else {
				file.Regenerated = nil
			}
		}
		result.Files = append(result.Files, file)
	}
	// Removed/renamed outputs require explicit review; never drop a manual buffer.
	for _, file := range request.Files {
		if !seen[file.Path] {
			result.Files = append(result.Files, file)
			result.Errors = append(result.Errors, domain.FieldError{Field: file.Path, Code: "OUTPUT_REMOVAL_REVIEW", Message: "Output path changed; review retained file before removal"})
		}
	}
	return result
}
func (e *Editor) Apply(result PreviewResult) bool {
	if result.Revision != e.Revision {
		return false
	}
	e.Files = result.Files
	return true
}
func (e *Editor) Edit(path, content string) error {
	for i := range e.Files {
		if e.Files[i].Path == path {
			e.Files[i].Content = content
			e.Files[i].ManuallyEdited = content != e.Files[i].GeneratedBaseline
			e.Dirty = true
			e.Revision++
			return nil
		}
	}
	return errors.New("file is not a generated draft output")
}

// EqualValues is useful to avoid rerendering unchanged fields, without reversing
// edited Nix source back into form values.
func EqualValues(a, b domain.Values) bool { return reflect.DeepEqual(a, b) }
