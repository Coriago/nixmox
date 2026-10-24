package service

import (
	"cmp"
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"nixmox/internal/domain"
)

type logQuery struct {
	TaskID domain.ID
	Offset int64
	Limit  int
}

func taskTerminal(state domain.TaskState) bool {
	return state == domain.Succeeded || state == domain.Failed || state == domain.Cancelled || state == domain.Blocked
}

type taskObservation struct {
	domain.Task
	Hash domain.Digest
}

func (o taskObservation) receipt() domain.Receipt {
	admission := "accepted"
	if o.State == domain.Queued {
		admission = "queued"
	}
	return domain.Receipt{CommandID: o.CommandID, TaskID: &o.ID, Admission: admission}
}

func (c *ClusterNode) observeTaskLocked(current domain.Task, hash domain.Digest) {
	if c.taskViews == nil {
		c.taskViews = map[domain.ID]taskObservation{}
	}
	old := c.taskViews[current.ID]
	if old.Version > current.Version {
		return
	}
	if hash == "" {
		hash = old.Hash
	}
	if old.ID == "" && len(c.taskViews) >= 1024 {
		clear(c.taskViews)
	} // Bounded observations, not history.
	c.taskViews[current.ID] = taskObservation{Task: current, Hash: hash}
}
func (s *Service) publishTask(ctx context.Context, current domain.Task) {
	c := s.Cluster
	if c == nil {
		return
	}
	c.mu.Lock()
	c.observeTaskLocked(current, "")
	core := c.transport.Core
	c.mu.Unlock()
	if core != nil {
		_ = core.PublishTask(ctx, current)
	}
}
func (s *Service) observeTask(current domain.Task) {
	c := s.Cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	if current.OwnerNodeID != s.Identity.NodeID {
		if c.state == nil {
			return
		}
		member, ok := c.state.Members[current.OwnerNodeID]
		if !ok || member.Removed || (current.Scope == domain.ClusterScope && member.Epoch != current.MembershipEpoch) {
			return
		}
	}
	c.observeTaskLocked(current, "")
}
func (s *Service) taskState(ctx context.Context, id domain.ID) (domain.Task, error) {
	current, err := s.Tasks.Task(ctx, id)
	if !errors.Is(err, os.ErrNotExist) || s.Cluster == nil {
		return current, err
	}
	c := s.Cluster
	if c.leaf() {
		err = c.callResult(ctx, "", "read", ReadRequest{Collection: "tasks", ID: &id}, true, &current)
		return current, err
	}
	c.mu.Lock()
	cached, exists := c.taskViews[id]
	c.mu.Unlock()
	if !exists {
		return current, notFound()
	}
	if taskTerminal(cached.State) || (cached.OwnerNodeID == s.Identity.NodeID && cached.State == domain.Queued && time.Since(cached.CreatedAt) < domain.CommandTTL) {
		return cached.Task, nil
	}
	err = c.callResult(ctx, cached.OwnerNodeID, "task.state", id, false, &current)
	if err == nil {
		return current, nil
	}
	if cached.State == domain.Queued && time.Since(cached.CreatedAt) < domain.CommandTTL {
		return cached.Task, nil
	}
	return domain.Task{}, &domain.Error{Code: "TASK_UNAVAILABLE", Message: "Executor session is unavailable; interrupted tasks are not recovered"}
}

// Only cursor encoding uses formatted timestamps; filtering/sorting use time.Time.
const listTimeFormat = "2006-01-02T15:04:05.000000000Z"

func (s *Service) taskList(ctx context.Context, request domain.PageRequest) (domain.Page[domain.Task], error) {
	request, err := validatePage("tasks", request)
	if err != nil {
		return domain.Page[domain.Task]{}, err
	}
	state, err := enumFilter(request.Filters["state"], domain.Queued, domain.Running, domain.Blocked, domain.Cancelling, domain.Cancelled, domain.Succeeded, domain.Failed)
	if err != nil {
		return domain.Page[domain.Task]{}, err
	}
	host, kind := domain.ID(request.Filters["host_id"]), request.Filters["kind"]
	if kind != "" {
		if _, ok := s.Registry.Lookup(kind); !ok {
			return domain.Page[domain.Task]{}, invalid("Unknown task kind")
		}
	}
	var bounds [2]*time.Time
	for i, name := range []string{"since", "until"} {
		if raw := request.Filters[name]; raw != "" {
			value, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return domain.Page[domain.Task]{}, invalid("Time filters require RFC3339 timestamps")
			}
			value = value.UTC()
			bounds[i] = &value
			request.Filters[name] = value.Format(listTimeFormat)
		}
	}
	since, until := bounds[0], bounds[1]
	if since != nil && until != nil && !since.Before(*until) {
		return domain.Page[domain.Task]{}, invalid("since must precede until")
	}
	byID := map[domain.ID]domain.Task{}
	if s.Cluster != nil {
		s.Cluster.mu.Lock()
		for id, current := range s.Cluster.taskViews {
			byID[id] = current.Task
		}
		s.Cluster.mu.Unlock()
	}
	for _, current := range s.Tasks.List() {
		byID[current.ID] = current
	}
	items := make([]domain.Task, 0, len(byID))
	for _, current := range byID {
		if (host != "" && current.TargetID != host) ||
			(kind != "" && current.Kind != kind) ||
			(state != "" && current.State != state) ||
			(since != nil && current.CreatedAt.Before(*since)) ||
			(until != nil && !current.CreatedAt.Before(*until)) {
			continue
		}
		items = append(items, current)
	}
	slices.SortFunc(items, func(a, b domain.Task) int { return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID)) })
	return pageItems(items, request, "tasks", func(t domain.Task) []string { return []string{t.CreatedAt.UTC().Format(listTimeFormat), string(t.ID)} })
}
func (s *Service) taskLogs(ctx context.Context, id domain.ID, offset int64, limit int) (domain.LogChunk, error) {
	current, err := s.taskState(ctx, id)
	if err != nil {
		return domain.LogChunk{}, err
	}
	if current.OwnerNodeID == s.Identity.NodeID {
		return s.Tasks.Read(ctx, id, nil, offset, limit)
	}
	var chunk domain.LogChunk
	err = s.Cluster.callResult(ctx, current.OwnerNodeID, "task.logs", logQuery{id, offset, limit}, false, &chunk)
	return chunk, err
}
