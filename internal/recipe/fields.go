package recipe

import (
	"context"
	"errors"
	"reflect"
)

// Validate checks a submitted value against the field and resolved choices.
func (f Field) Validate(ctx context.Context, value any, inputs map[string]any) error {
	switch f.Type {
	case "text":
		if _, ok := value.(string); ok {
			return nil
		}
	case "boolean":
		if _, ok := value.(bool); ok {
			return nil
		}
	case "select", "multiselect":
		return f.validateSelection(ctx, value, inputs)
	}
	return Invalid(errors.New("invalid field value"))
}

func (f Field) validateSelection(ctx context.Context, value any, inputs map[string]any) error {
	choices, err := Evaluate(ctx, f.Values, inputs)
	if err != nil {
		return Invalid(err)
	}
	options, ok := choices.([]any)
	if !ok {
		return Invalid(errors.New("choices must resolve to a list"))
	}

	selected := []any{value}
	if f.Type == "multiselect" {
		selected, ok = value.([]any)
		if !ok {
			return Invalid(errors.New("multiselect value must be a list"))
		}
	}
	for _, item := range selected {
		matches, err := f.matchesChoice(ctx, item, options)
		if err != nil {
			return Invalid(err)
		}
		if !matches {
			return Invalid(errors.New("selection is not an available choice"))
		}
	}
	return nil
}

func (f Field) matchesChoice(ctx context.Context, value any, options []any) (bool, error) {
	for _, option := range options {
		if f.OptionValue != "" {
			resolved, err := Evaluate(ctx, f.OptionValue, option)
			if err != nil {
				return false, err
			}
			option = resolved
		}
		if reflect.DeepEqual(option, value) {
			return true, nil
		}
	}
	return false, nil
}
