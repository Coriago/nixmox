package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
type pageCursor struct {
	Filter string `json:"filter"`
	ID     string `json:"id"`
}

func PageLimit(raw string) (int, error) {
	if raw == "" {
		return 100, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 500 {
		return 0, Err("INVALID_QUERY", "Limit must be between 1 and 500.", 400)
	}
	return limit, nil
}
func Paginate[T any](items []T, binding any, rawLimit, rawCursor string, key func(T) string) (Page[T], error) {
	page := Page[T]{Items: []T{}}
	limit, err := PageLimit(rawLimit)
	if err != nil {
		return page, err
	}
	b, _ := json.Marshal(binding)
	hash := sha256.Sum256(b)
	filter := hex.EncodeToString(hash[:])
	after := ""
	if rawCursor != "" {
		if len(rawCursor) > 1024 {
			return page, Err("INVALID_QUERY", "Invalid cursor.", 400)
		}
		b, err := base64.RawURLEncoding.DecodeString(rawCursor)
		var cursor pageCursor
		if err != nil || StrictJSON(b, &cursor) != nil || cursor.Filter != filter || !SafeID.MatchString(cursor.ID) {
			return page, Err("INVALID_QUERY", "Cursor does not match this query.", 400)
		}
		after = cursor.ID
	}
	found := after == ""
	for _, item := range items {
		if !found {
			if key(item) == after {
				found = true
			}
			continue
		}
		if len(page.Items) == limit {
			b, _ := json.Marshal(pageCursor{Filter: filter, ID: key(page.Items[limit-1])})
			cursor := base64.RawURLEncoding.EncodeToString(b)
			page.NextCursor = &cursor
			break
		}
		page.Items = append(page.Items, item)
	}
	if !found {
		return page, Err("CURSOR_EXPIRED", "Cursor position is no longer available.", 410)
	}
	return page, nil
}
