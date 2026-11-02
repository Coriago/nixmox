package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/sync/errgroup"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/util"
)

var ErrReview = errors.New("task awaiting manual review")

type Runner func(context.Context, *Execution) (any, error)
type Manager struct {
	HostID    string
	Tasks     cluster.Store[domain.Task]
	Outputs   cluster.Store[domain.TaskOutput]
	Resources Resources
	Runners   map[string]Runner // Immutable after Run begins.
	mu        sync.Mutex
	active    map[string]context.CancelFunc
	wg        sync.WaitGroup
}

// Submit persists work; its execution lifetime belongs to Run, not this request.
func (m *Manager) Submit(ctx context.Context, task domain.Task) (domain.Task, error) {
	if task.HostID == "" || task.Type == "" {
		return task, errors.New("hostId and tasktype required")
	}
	if !json.Valid(task.Inputs) {
		return task, errors.New("inputs must be valid JSON")
	}
	task.ID = uuid.NewString()
	task.Status = domain.Queued
	task.CreatedAt = time.Now().UTC()
	task.StartedAt = nil
	task.FinishedAt = nil
	task.Review = nil
	task.Result = nil
	task.Error = nil
	task.Context = map[string]json.RawMessage{}
	task.Trace = cluster.DispatchContext(ctx)
	task.Links.ChildTasks = []domain.ChildTask{}
	task.Links.Outputs = []domain.OutputRef{}
	_, err := m.Tasks.Create(ctx, task.ID, task)
	return task, err
}

func (m *Manager) Retry(ctx context.Context, id string) (domain.Task, error) {
	record, err := m.Tasks.Get(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if !record.Value.Status.Terminal() {
		return domain.Task{}, errors.New("only terminal tasks can be retried")
	}
	task := record.Value
	task.Links.RetryOfTaskID = id
	return m.Submit(ctx, task)
}

func (m *Manager) owned(ctx context.Context, id string) (cluster.Record[domain.Task], error) {
	record, err := m.Tasks.Get(ctx, id)
	if err == nil && record.Value.HostID != m.HostID {
		err = errors.New("task mutation must run on owning host")
	}
	return record, err
}

// Cancel stops the local runner first. Its completion records cancellation, so
// resources are never released while a cancelled subprocess can still run.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.owned(ctx, id)
	if err != nil {
		return err
	}
	if record.Value.Status.Terminal() {
		return errors.New("task already terminal")
	}
	if cancel := m.active[id]; cancel != nil {
		cancel()
		return nil
	}
	return m.finish(ctx, record, domain.Cancelled, nil, nil)
}

func (m *Manager) Approve(ctx context.Context, id, outputID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.owned(ctx, id)
	if err != nil {
		return err
	}
	task := record.Value
	if task.Status != domain.ManualReview || task.Review == nil || task.Review.OutputID != outputID || task.Review.ApprovedAt != nil {
		return errors.New("review is stale or not awaiting approval")
	}
	now := time.Now().UTC()
	task.Review.ApprovedAt = &now
	_, err = m.Tasks.Update(ctx, id, task, record.Revision)
	return err
}

// Run consumes native KV watches. Reconciliation occurs before starting any
// runner. A caller must hold the process identity lock for this HostID.
func (m *Manager) Run(ctx context.Context) error {
	lifetime, cancel := context.WithCancel(ctx)
	defer func() { cancel(); m.wg.Wait() }()
	m.mu.Lock()
	m.active = map[string]context.CancelFunc{}
	m.mu.Unlock()
	if err := m.reconcile(lifetime); err != nil {
		return err
	}
	group, watchCtx := errgroup.WithContext(lifetime)
	group.Go(func() error {
		return m.Tasks.Watch(watchCtx, ">", func(_ *cluster.Record[domain.Task], _ bool) error { return m.schedule(watchCtx) })
	})
	group.Go(func() error {
		return m.Resources.Store.Watch(watchCtx, "resources", func(_ *cluster.Record[map[string]string], _ bool) error { return m.schedule(watchCtx) })
	})
	return group.Wait()
}

func (m *Manager) each(ctx context.Context, visit func(cluster.Record[domain.Task]) error) error {
	cursor := ""
	for {
		records, next, err := m.Tasks.List(ctx, cursor, 100)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err = visit(record); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
}

func (m *Manager) reconcile(ctx context.Context) error {
	return m.each(ctx, func(record cluster.Record[domain.Task]) error {
		if record.Value.HostID != m.HostID {
			return nil
		}
		if record.Value.Status == domain.Running {
			return m.finish(ctx, record, domain.Unknown, nil, errors.New("runner interrupted; inspect effects before retrying"))
		}
		if record.Value.Status.Terminal() {
			return m.Resources.Release(ctx, record.Key)
		}
		return nil
	})
}

func (m *Manager) schedule(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.each(ctx, func(record cluster.Record[domain.Task]) error {
		task := record.Value
		if task.HostID != m.HostID || m.active[task.ID] != nil {
			return nil
		}
		if task.Status == domain.Running {
			return m.finish(ctx, record, domain.Unknown, nil, errors.New("running task has no local runner"))
		}
		if task.Status != domain.Queued && !(task.Status == domain.ManualReview && task.Review != nil && task.Review.ApprovedAt != nil) {
			return nil
		}
		for _, id := range task.Links.AfterSuccessOfTaskIDs {
			dependency, err := m.Tasks.Get(ctx, id)
			if err != nil {
				return err
			}
			if dependency.Value.Status == domain.Succeeded {
				continue
			}
			if dependency.Value.Status.Terminal() {
				return m.finish(ctx, record, domain.Cancelled, nil, errors.New("dependency did not succeed"))
			}
			return nil
		}
		runner := m.Runners[task.Type]
		if runner == nil {
			return m.finish(ctx, record, domain.Failed, nil, errors.New("unsupported task type on owning host"))
		}
		if err := m.Resources.Acquire(ctx, task.ID, task.Concurrency.ExclusiveResources); err != nil {
			if errors.Is(err, ErrBusy) {
				return nil
			}
			return err
		}
		task.Status = domain.Running
		if task.StartedAt == nil {
			now := time.Now().UTC()
			task.StartedAt = &now
		}
		revision, err := m.Tasks.Update(ctx, task.ID, task, record.Revision)
		if err != nil {
			releaseErr := m.Resources.Release(ctx, task.ID)
			if errors.Is(err, jetstream.ErrKeyExists) {
				return releaseErr
			}
			return errors.Join(err, releaseErr)
		}
		runCtx, cancel := context.WithCancel(cluster.ResumeContext(ctx, propagation.MapCarrier(task.Trace)))
		m.active[task.ID] = cancel
		m.wg.Add(1)
		go m.execute(ctx, runCtx, cancel, runner, cluster.Record[domain.Task]{Key: task.ID, Value: task, Revision: revision})
		return nil
	})
}

func (m *Manager) execute(lifetime, ctx context.Context, cancel context.CancelFunc, runner Runner, record cluster.Record[domain.Task]) {
	defer m.wg.Done()
	defer cancel()
	ctx, span := otel.Tracer("nixmox.task").Start(ctx, "task.execute")
	defer span.End()
	execution := &Execution{manager: m, record: record}
	var result any
	var runErr error
	func() {
		defer func() {
			if recover() != nil {
				runErr = errors.New("task runner panicked")
			}
		}()
		result, runErr = runner(ctx, execution)
	}()
	// Finish even when the runner was cancelled, with a bounded independent write.
	finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		if lifetime.Err() == nil {
			if err := m.schedule(lifetime); err != nil && !errors.Is(err, context.Canceled) {
				util.From(ctx).Error("task scheduling failed", "error", err)
			}
		}
	}()
	delete(m.active, record.Key)
	if errors.Is(runErr, ErrReview) && ctx.Err() == nil {
		paused := execution.record.Value
		paused.Status = domain.ManualReview
		if err := execution.save(finishCtx, paused); err != nil {
			util.From(ctx).Error("task review persistence failed", "task", record.Key, "error", err)
		}
		return
	}
	status := domain.Succeeded
	if runErr != nil {
		status = domain.Failed
	}
	if ctx.Err() != nil {
		status = domain.Cancelled
	}
	if lifetime.Err() != nil {
		status = domain.Unknown
		runErr = errors.New("daemon stopped during execution; inspect effects before retrying")
	}
	if err := m.finish(finishCtx, execution.record, status, result, runErr); err != nil {
		util.From(ctx).Error("task completion persistence failed", "task", record.Key, "error", err)
	}
}

func (m *Manager) finish(ctx context.Context, record cluster.Record[domain.Task], status domain.TaskStatus, result any, runErr error) error {
	task := record.Value
	task.Status = status
	now := time.Now().UTC()
	task.FinishedAt = &now
	if runErr != nil {
		task.Error = &domain.TaskError{Message: util.Error(runErr).Error()}
	}
	if result != nil {
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		task.Result = data
	}
	if _, err := m.Tasks.Update(ctx, record.Key, task, record.Revision); err != nil {
		return err
	}
	return m.Resources.Release(ctx, record.Key)
}

type Execution struct {
	manager *Manager
	record  cluster.Record[domain.Task]
}

func (e *Execution) Task() domain.Task { return e.record.Value }
func (e *Execution) Checkpoint(ctx context.Context, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	task := e.record.Value
	task.Context[key] = data
	return e.save(ctx, task)
}
func (e *Execution) save(ctx context.Context, task domain.Task) error {
	revision, err := e.manager.Tasks.Update(ctx, e.record.Key, task, e.record.Revision)
	if err == nil {
		e.record.Value = task
		e.record.Revision = revision
	}
	return err
}

// Review commits an immutable structured output before entering the review
// state. On resumed execution, runners reuse checkpointed build results.
func (e *Execution) Review(ctx context.Context, kind string, content any) error {
	if e.record.Value.Review != nil {
		if e.record.Value.Review.ApprovedAt != nil {
			return nil
		}
		return ErrReview
	}
	data, err := json.Marshal(content)
	if err != nil {
		return err
	}
	output := domain.TaskOutput{ID: uuid.NewString(), TaskID: e.record.Key, Kind: kind, Content: data}
	if _, err = e.manager.Outputs.Create(ctx, output.ID, output); err != nil {
		return err
	}
	task := e.record.Value
	// The manager publishes manual-review only after the runner has returned.
	// Approval and cancellation therefore cannot race a still-active runner.
	task.Review = &domain.Review{OutputID: output.ID}
	task.Links.Outputs = append(task.Links.Outputs, domain.OutputRef{ID: output.ID, Kind: kind})
	if err = e.save(ctx, task); err != nil {
		return fmt.Errorf("persist review: %w", err)
	}
	return ErrReview
}
