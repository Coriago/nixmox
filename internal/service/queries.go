package service

import (
	"nixmox/internal/domain"
	"sort"
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
	tasks := s.Tasks()
	s.mu.Lock()
	transport, own := s.clusterTransport, s.identity.NodeID
	s.mu.Unlock()
	if reader, ok := transport.(RemoteTaskReader); ok && f.HostID != own {
		remote, err := reader.RemoteTasks()
		if err != nil {
			return page, err
		}
		tasks = append(tasks, remote...)
		sort.Slice(tasks, func(i, j int) bool {
			if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
				return tasks[i].ID > tasks[j].ID
			}
			return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
		})
	}
	for _, task := range tasks {
		if (f.HostID != "" && task.TargetID != f.HostID) || (f.Kind != "" && task.Kind != f.Kind) || (f.State != "" && task.State != f.State) || (!since.IsZero() && task.CreatedAt.Before(since)) || (!until.IsZero() && !task.CreatedAt.Before(until)) {
			continue
		}
		filtered = append(filtered, task)
	}
	return domain.Paginate(filtered, f, rawLimit, rawCursor, func(t domain.Task) string { return t.ID })
}
