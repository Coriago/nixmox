package service

import (
	"nixmox/internal/domain"
	"time"
)

type TaskFilter struct{ HostID, Kind, State, Since, Until string }

func (s *Service) ListTasks(f TaskFilter, rawLimit, rawCursor string) (domain.Page[domain.Task], error) {
	page := domain.Page[domain.Task]{Items: []domain.Task{}}
	bad := domain.Err("INVALID_QUERY", "Invalid task filter, cursor or limit.", 400)
	if _, err := domain.PageLimit(rawLimit); err != nil {
		return page, err
	}
	if f.HostID != "" && !domain.SafeID.MatchString(f.HostID) {
		return page, bad
	}
	if f.Kind != "" {
		if _, ok := s.operations[f.Kind]; !ok {
			return page, bad
		}
	}
	if f.State != "" {
		valid := false
		for _, state := range []string{"queued", "running", "blocked", "reconciling", "cancelling", "cancelled", "succeeded", "failed"} {
			if f.State == state {
				valid = true
			}
		}
		if !valid {
			return page, bad
		}
	}
	var since, until time.Time
	var err error
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
	filtered := []domain.Task{}
	for _, task := range s.Tasks() {
		if (f.HostID != "" && task.TargetID != f.HostID) || (f.Kind != "" && task.Kind != f.Kind) || (f.State != "" && task.State != f.State) || (!since.IsZero() && task.CreatedAt.Before(since)) || (!until.IsZero() && !task.CreatedAt.Before(until)) {
			continue
		}
		filtered = append(filtered, task)
	}
	return domain.Paginate(filtered, f, rawLimit, rawCursor, func(t domain.Task) string { return t.ID })
}
