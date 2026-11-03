package recipe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"text/template"
	"text/template/parse"
)

const RenderLimit = 1 << 20

type boundedText struct {
	bytes.Buffer
	ctx context.Context
}

func (b *boundedText) Write(data []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if b.Len()+len(data) > RenderLimit {
		return 0, errors.New("rendered value exceeds 1 MiB")
	}
	return b.Buffer.Write(data)
}

func parseTemplate(text string) (*template.Template, error) {
	return template.New("recipe").Option("missingkey=error").Parse(text)
}

func singleAction(t *template.Template) *parse.ActionNode {
	var action *parse.ActionNode
	for _, node := range t.Tree.Root.Nodes {
		if text, ok := node.(*parse.TextNode); ok && strings.TrimSpace(string(text.Text)) == "" {
			continue
		}
		current, ok := node.(*parse.ActionNode)
		if !ok || action != nil || len(current.Pipe.Decl) != 0 {
			return nil
		}
		action = current
	}
	return action
}

// Text uses Go's template engine directly. Data inserted into a template is not
// reparsed, preventing accidental interpolation of literal user-supplied text.
func Text(ctx context.Context, text string, data any) (string, error) {
	t, err := parseTemplate(text)
	if err != nil {
		return "", err
	}
	output := &boundedText{ctx: ctx}
	if err = t.Execute(output, data); err != nil {
		return "", err
	}
	return output.String(), nil
}

// Evaluate preserves the type of a whole-value Go template pipeline (including
// lists, maps and booleans). Mixed text evaluates to a string. Evaluation is
// delegated to text/template, not a parallel expression language.
func Evaluate(ctx context.Context, value, data any) (any, error) {
	switch value := value.(type) {
	case string:
		if !strings.Contains(value, "{{") {
			return value, nil
		}
		t, err := parseTemplate(value)
		if err != nil {
			return nil, err
		}
		if action := singleAction(t); action != nil {
			var captured any
			capture := func(value any) (string, error) {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				captured = value
				return "", nil
			}
			typed, err := template.New("value").Option("missingkey=error").Funcs(template.FuncMap{"capture": capture}).Parse("{{capture (" + action.Pipe.String() + ")}}")
			if err != nil {
				return nil, err
			}
			if err = typed.Execute(io.Discard, data); err != nil {
				return nil, err
			}
			return captured, nil
		}
		return Text(ctx, value, data)
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			resolved, err := Evaluate(ctx, item, data)
			if err != nil {
				return nil, err
			}
			result[i] = resolved
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(value))
		for _, key := range slices.Sorted(maps.Keys(value)) {
			resolved, err := Evaluate(ctx, value[key], data)
			if err != nil {
				return nil, err
			}
			result[key] = resolved
		}
		return result, nil
	default:
		return value, nil
	}
}
