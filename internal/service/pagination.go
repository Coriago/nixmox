package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"nixmox/internal/domain"
)

// ParsePageQuery handles transport syntax only. Services validate the collection's
// filter vocabulary too, so HTTP and forwarded NATS reads share the same rules.
func ParsePageQuery(values url.Values) (domain.PageRequest, error) {
	p := domain.PageRequest{Filters: map[string]string{}}
	for key, values := range values {
		if len(values) != 1 {
			return p, invalid("Repeated query parameter")
		}
		value := values[0]
		switch key {
		case "cursor":
			p.Cursor = value
		case "limit":
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 1 || limit > domain.PageLimit {
				return p, invalid("Invalid page limit")
			}
			p.Limit = limit
		default:
			p.Filters[key] = value
		}
	}
	return p, nil
}

func collectionFor(path string) Collection {
	for _, c := range Collections {
		if c.Path == path {
			return c
		}
	}
	return Collection{Path: path}
}

func validatePage(path string, p domain.PageRequest) (domain.PageRequest, error) {
	if p.Limit == 0 {
		p.Limit = domain.PageDefault
	}
	if p.Limit < 1 || p.Limit > domain.PageLimit || len(p.Cursor) > 8192 {
		return p, invalid("Invalid pagination bounds")
	}
	filters := make(map[string]string, len(p.Filters))
	for key, value := range p.Filters {
		if !slices.Contains(collectionFor(path).Filters, key) || value == "" || len(value) > 512 || strings.ContainsFunc(value, unicode.IsControl) {
			return p, invalid("Invalid or unsupported filter: " + key)
		}
		filters[key] = value
	}
	p.Filters = filters
	return p, nil
}

type listCursor struct {
	Version int      `json:"v"`
	Binding string   `json:"binding"`
	Key     []string `json:"key"`
}

// pageItems consumes a filtered, sorted snapshot without mutating it. key encodes
// the ordering position (including ID), with a fixed arity even for a zero T.
// Ordering metadata is only a cursor binding, never an interpreted expression.
// Cursors are not authorization tokens; vanished/changed positions expire.
func pageItems[T any](items []T, p domain.PageRequest, path string, key func(T) []string) (domain.Page[T], error) {
	result := domain.Page[T]{Items: []T{}}
	p, err := validatePage(path, p)
	if err != nil {
		return result, err
	}
	collection := collectionFor(path)
	if collection.Order == "" {
		return result, invalid("Collection has no list ordering")
	}
	raw, _ := json.Marshal(struct {
		Path, Order string
		Filters     map[string]string
	}{path, collection.Order, p.Filters})
	digest := sha256.Sum256(raw)
	binding := hex.EncodeToString(digest[:])
	var cursor listCursor
	var zero T
	if p.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(p.Cursor)
		if err != nil {
			return result, invalid("Invalid cursor")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.Binding != binding || len(cursor.Key) != len(key(zero)) {
			return result, invalid("Cursor does not match collection, filters or ordering")
		}
	}
	start := 0
	if p.Cursor != "" {
		index := slices.IndexFunc(items, func(item T) bool { return slices.Equal(key(item), cursor.Key) })
		if index < 0 {
			return result, &domain.Error{Code: "CURSOR_EXPIRED", Message: "Read cursor position is no longer available"}
		}
		start = index + 1
	}
	end := min(start+p.Limit, len(items))
	result.Items = append(result.Items, items[start:end]...)
	if end < len(items) {
		data, _ := json.Marshal(listCursor{Version: 1, Binding: binding, Key: key(items[end-1])})
		next := base64.RawURLEncoding.EncodeToString(data)
		result.NextCursor = &next
	}
	return result, nil
}

// enumFilter keeps optional enum decoding shared without knowing any resource.
func enumFilter[T ~string](raw string, allowed ...T) (T, error) {
	value := T(raw)
	if raw != "" && !slices.Contains(allowed, value) {
		return value, invalid("Invalid filter value")
	}
	return value, nil
}
