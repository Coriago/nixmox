package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"time"

	js "github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/task"
	"nixmox/internal/engine/telemetry"
)

// Completion and definitive failure evidence use the same journal boundary.
// Callers must not observe a storage-uncertain effect as a definitive failure.
func completePhase(ctx context.Context, e engine.Execution, phase string, result any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := e.Result(ctx, data); err != nil {
		return err
	}
	return e.Observed(ctx, domain.Checkpoint{Phase: phase, Evidence: data})
}

func observeFailure(ctx context.Context, e engine.Execution, phase string, failure error) error {
	data, err := json.Marshal(map[string]any{"error": failure})
	if err != nil {
		return err
	}
	if err := e.Observed(ctx, domain.Checkpoint{Phase: phase, Evidence: data}); err != nil {
		return err
	}
	return failure
}

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
	return listRecords[domain.Task](call, transport.Store, "task.*")
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
		_ = s.reconcileTasks(ctx)
	}
}
func (s *Service) reconcileTasks(ctx context.Context) (resultErr error) {
	ctx, span := telemetry.Background(ctx, "tasks.reconcile")
	defer func() { telemetry.End(span, resultErr) }()
	items, err := s.Cluster.projectedTasks(ctx)
	if err != nil {
		return err
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
			_, err := s.Cluster.refreshTask(ctx, current)
			resultErr = errors.Join(resultErr, err)
		}
	}
	return errors.Join(resultErr, s.Cluster.reconcilePower(ctx))
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

// Only cursor encoding uses formatted timestamps; filtering/sorting use time.Time.
const listTimeFormat = "2006-01-02T15:04:05.000000000Z"

func (s *Service) taskList(ctx context.Context, request domain.PageRequest) (domain.Page[domain.Task], error) {
	request, err := validatePage("tasks", request)
	if err != nil {
		return domain.Page[domain.Task]{}, err
	}
	state, err := enumFilter(request.Filters["state"], domain.Queued, domain.Running, domain.Blocked, domain.Reconciling, domain.Cancelling, domain.Cancelled, domain.Succeeded, domain.Failed)
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
		return s.Journal.Read(ctx, id, nil, offset, limit)
	}
	data, err := s.Cluster.call(ctx, current.OwnerNodeID, "task.logs", logQuery{id, offset, limit}, false)
	var chunk domain.LogChunk
	if err == nil {
		err = json.Unmarshal(data, &chunk)
	}
	return chunk, err
}
