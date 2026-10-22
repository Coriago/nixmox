package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"nixmox/internal/domain"
)

type TaskFilter struct{ HostID, Kind, State, Since, Until string }
type TaskPage struct {
	Items      []domain.Task `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}
type cursor struct {
	Filter string `json:"filter"`
	ID     string `json:"id"`
}

func (s *Service) ListTasks(f TaskFilter, rawLimit, rawCursor string) (TaskPage, error) {
	page := TaskPage{Items: []domain.Task{}}
	bad := domain.Err("INVALID_QUERY", "Invalid task filter, cursor or limit.", 400)
	limit := 100
	var err error
	if rawLimit != "" {
		limit, err = strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > 500 {
			return page, bad
		}
	}
	if f.HostID != "" && !domain.SafeID.MatchString(f.HostID) {
		return page, bad
	}
	if f.Kind != "" && f.Kind != "network.check" {
		return page, bad
	}
	if f.State != "" {
		valid := false
		for _, v := range []string{"queued", "running", "blocked", "reconciling", "cancelling", "cancelled", "succeeded", "failed"} {
			if f.State == v {
				valid = true
			}
		}
		if !valid {
			return page, bad
		}
	}
	var since, until time.Time
	if f.Since != "" {
		since, err = time.Parse(time.RFC3339Nano, f.Since)
		if err != nil {
			return page, bad
		}
	}
	if f.Until != "" {
		until, err = time.Parse(time.RFC3339Nano, f.Until)
		if err != nil {
			return page, bad
		}
	}
	if !since.IsZero() && !until.IsZero() && !since.Before(until) {
		return page, bad
	}
	b, _ := json.Marshal(f)
	sum := sha256.Sum256(b)
	binding := hex.EncodeToString(sum[:])
	after := ""
	if rawCursor != "" {
		if len(rawCursor) > 1024 {
			return page, bad
		}
		b, err := base64.RawURLEncoding.DecodeString(rawCursor)
		if err != nil {
			return page, bad
		}
		var c cursor
		if domain.StrictJSON(b, &c) != nil || c.Filter != binding || !domain.SafeID.MatchString(c.ID) {
			return page, bad
		}
		after = c.ID
	}
	found := after == ""
	filtered := []domain.Task{}
	for _, t := range s.Tasks() {
		if (f.HostID != "" && t.TargetID != f.HostID) || (f.Kind != "" && t.Kind != f.Kind) || (f.State != "" && t.State != f.State) || (!since.IsZero() && t.CreatedAt.Before(since)) || (!until.IsZero() && !t.CreatedAt.Before(until)) {
			continue
		}
		if !found {
			if t.ID == after {
				found = true
			}
			continue
		}
		filtered = append(filtered, t)
	}
	if !found {
		return page, domain.Err("CURSOR_EXPIRED", "Cursor position is no longer available.", 410)
	}
	if len(filtered) > limit {
		b, _ := json.Marshal(cursor{Filter: binding, ID: filtered[limit-1].ID})
		next := base64.RawURLEncoding.EncodeToString(b)
		page.NextCursor = &next
		filtered = filtered[:limit]
	}
	page.Items = filtered
	return page, nil
}
