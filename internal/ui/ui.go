// Package ui contains small presentation utilities shared by components and views.
package ui

import (
	"slices"
	"strings"
	"unicode"

	"nixmox/internal/domain"
)

type Action struct{ Kind, ID string }

// Find returns the first matching slice element, not a copy.
func Find[T any](items []T, match func(T) bool) *T {
	if i := slices.IndexFunc(items, match); i >= 0 {
		return &items[i]
	}
	return nil
}

// Cycle selects a neighboring item; -1 means no current selection.
func Cycle(index, step, count int) int {
	if count == 0 {
		return -1
	}
	if index < 0 {
		if step > 0 {
			return 0
		}
		return count - 1
	}
	return (index + step + count) % count
}

func Active(state string) bool {
	switch domain.TaskState(state) {
	case domain.Running, domain.Cancelling:
		return true
	}
	return false
}

// Text removes controls/formatting characters; a negative replacement deletes them.
// Limits are rune-based, so truncation never splits UTF-8.
func Text(value string, limit int, replacement rune) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return replacement
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}
