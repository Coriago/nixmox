package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"

	js "github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/domain"
	"nixmox/internal/engine/task"
)

type logQuery struct {
	TaskID domain.ID
	Offset int64
	Limit  int
}

func taskTerminal(state domain.TaskState) bool {
	return state == domain.Succeeded || state == domain.Failed || state == domain.Cancelled
}

// Projections are versioned copies of executor evidence, never execution owners.
func (c *ClusterNode) projectTask(ctx context.Context, envelope domain.Envelope, current domain.Task) error {
	if current.ID != task.TaskID(envelope.Command.ID) || current.CommandID != envelope.Command.ID || current.OwnerNodeID != envelope.OwnerNodeID || current.Scope != domain.ClusterScope || current.ClusterID == nil || envelope.ClusterID == nil || *current.ClusterID != *envelope.ClusterID || current.Version == 0 {
		return invalid("Task projection owner/assignment mismatch")
	}
	key := recordKey("task", current.ID)
	old, revision, err := readRecord[domain.Task](ctx, c.transport.Store, key)
	if err != nil && !errors.Is(err, js.ErrKeyNotFound) {
		return err
	}
	if err == nil && old.Version >= current.Version {
		return nil
	}
	if err == nil && taskTerminal(old.State) {
		return invalid("Terminal task projection cannot change")
	}
	_, err = writeRecord(ctx, c.transport.Store, key, revision, current)
	return err
}
func (c *ClusterNode) projectedTasks(ctx context.Context) ([]domain.Task, error) {
	c.mu.Lock()
	transport := c.transport
	c.mu.Unlock()
	if transport.Store == nil {
		return nil, nil
	}
	call, cancel := context.WithTimeout(ctx, domain.ExportTimeout)
	defer cancel()
	entries, err := transport.Store.Entries(call, "task.*")
	if err != nil {
		return nil, err
	}
	tasks := make([]domain.Task, 0, len(entries))
	for _, entry := range entries {
		var current domain.Task
		if err = json.Unmarshal(entry.Value, &current); err != nil {
			return nil, err
		}
		tasks = append(tasks, current)
	}
	return tasks, nil
}
func (c *ClusterNode) projectedTask(ctx context.Context, id domain.ID) (domain.Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport.Store == nil {
		return domain.Task{}, notFound()
	}
	current, _, err := readRecord[domain.Task](ctx, c.transport.Store, recordKey("task", id))
	if errors.Is(err, js.ErrKeyNotFound) {
		err = notFound()
	}
	return current, err
}
func (c *ClusterNode) refreshTask(ctx context.Context, current domain.Task) (domain.Task, error) {
	value, err := c.call(ctx, current.OwnerNodeID, "task.state", current.ID, false)
	if err != nil {
		return domain.Task{}, err
	}
	var observed domain.Task
	if err = json.Unmarshal(value, &observed); err != nil {
		return observed, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err = c.quorum(ctx); err != nil {
		return observed, err
	}
	envelope, _, err := readRecord[domain.Envelope](ctx, c.transport.Store, recordKey("command", current.CommandID))
	if err != nil {
		return observed, err
	}
	return observed, c.projectTask(ctx, envelope, observed)
}

// Reconcile task projections after disconnect/restart by querying the owner.
// Terminal outcomes remain readable while their owner is offline; incomplete
// owner reads fail explicitly instead of pretending an old projection is live.
func (s *Service) ReconcileTasks(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		items, err := s.Cluster.projectedTasks(ctx)
		if err != nil {
			continue
		}
		for _, current := range items {
			if taskTerminal(current.State) {
				continue
			}
			if current.OwnerNodeID == s.Identity.NodeID {
				observed, err := s.Journal.Task(ctx, current.ID)
				if err != nil {
					continue
				}
				record, err := s.Journal.Receipt(ctx, current.CommandID)
				if err != nil {
					continue
				}
				c := s.Cluster
				c.mu.Lock()
				if c.quorum(ctx) == nil {
					_ = c.projectTask(ctx, record.Envelope, observed)
				}
				c.mu.Unlock()
			} else {
				_, _ = s.Cluster.refreshTask(ctx, current)
			}
		}
	}
}
func (s *Service) taskState(ctx context.Context, id domain.ID) (domain.Task, error) {
	current, err := s.Journal.Task(ctx, id)
	if !errors.Is(err, os.ErrNotExist) || s.Cluster == nil {
		return current, err
	}
	current, err = s.Cluster.projectedTask(ctx, id)
	if err != nil || taskTerminal(current.State) {
		return current, err
	}
	return s.Cluster.refreshTask(ctx, current)
}
func (s *Service) taskList(ctx context.Context, request domain.PageRequest) (domain.Page[domain.Task], error) {
	byID := map[domain.ID]domain.Task{}
	cursor := ""
	for {
		page, err := s.Journal.Tasks(ctx, domain.PageRequest{Cursor: cursor, Limit: domain.PageLimit})
		if err != nil {
			return domain.Page[domain.Task]{}, err
		}
		for _, current := range page.Items {
			byID[current.ID] = current
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if s.Cluster != nil {
		remote, err := s.Cluster.projectedTasks(ctx)
		if err != nil {
			return domain.Page[domain.Task]{}, err
		}
		for _, current := range remote {
			if prior, ok := byID[current.ID]; !ok || current.Version > prior.Version {
				byID[current.ID] = current
			}
		}
	}
	items := make([]domain.Task, 0, len(byID))
	for _, current := range byID {
		items = append(items, current)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return pageItems(items, request, func(t domain.Task) domain.ID { return t.ID })
}
func (s *Service) taskLogs(ctx context.Context, id domain.ID, offset int64, limit int) (domain.LogChunk, error) {
	current, err := s.taskState(ctx, id)
	if err != nil {
		return domain.LogChunk{}, err
	}
	if current.OwnerNodeID == s.Identity.NodeID {
		return s.Journal.Read(ctx, id, nil, offset, limit)
	}
	data, err := s.Cluster.call(ctx, current.OwnerNodeID, "task.logs", logQuery{id, offset, limit}, false)
	var chunk domain.LogChunk
	if err == nil {
		err = json.Unmarshal(data, &chunk)
	}
	return chunk, err
}
