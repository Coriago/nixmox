package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/sync/errgroup"

	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/util"
)

var ErrReview = errors.New("task awaiting manual review")

// ErrRestart suspends a persistent runner until a new daemon lifetime. It does
// not release resource ownership or report completion before post-boot validation.
var ErrRestart = errors.New("task awaiting daemon restart")

type Runner func(context.Context, *Execution) (any, error)
type Manager struct {
	HostID    string
	Journal   *Journal
	Tasks     cluster.Store[domain.Task]
	Outputs   cluster.Store[domain.TaskOutput]
	Logs      cluster.Logs
	Resources Resources
	Runners   map[string]Runner // Immutable after Run begins.
	mu        sync.Mutex
	active    map[string]context.CancelFunc
	suspended map[string]bool
	wg        sync.WaitGroup
}

// Submit persists work; its execution lifetime belongs to Run, not this request.
func (m *Manager) Submit(ctx context.Context, task domain.Task) (domain.Task, error) {
	return m.submit(ctx, task, task.HostID+"."+uuid.NewString())
}

// SubmitChild has a deterministic identity so a lost reply or daemon restart
// cannot orphan a second copy before its parent records the child link.
func (m *Manager) SubmitChild(ctx context.Context, task domain.Task, role string) (domain.Task, error) {
	if task.Links.ParentTaskID == "" || role == "" {
		return task, errors.New("child parent and role required")
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(task.Links.ParentTaskID+"\x00"+role)).String()
	return m.submit(ctx, task, task.HostID+"."+id)
}

func (m *Manager) submit(ctx context.Context, task domain.Task, id string) (domain.Task, error) {
	if task.HostID == "" || task.Type == "" {
		return task, errors.New("hostId and tasktype required")
	}
	if task.Persist && task.HostID == m.HostID && m.Journal == nil {
		return task, errors.New("persistent tasks require the owning host's journal")
	}
	if !json.Valid(task.Inputs) {
		return task, errors.New("inputs must be valid JSON")
	}
	if strings.ContainsAny(task.HostID, ".*> /\\\t\r\n") {
		return task, errors.New("invalid task host identity")
	}
	task.ID = id
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
	if errors.Is(err, jetstream.ErrKeyExists) && task.Links.ParentTaskID != "" {
		existing, readErr := m.Tasks.Get(ctx, task.ID)
		if readErr != nil {
			return task, readErr
		}
		value := existing.Value
		if value.HostID != task.HostID || value.Type != task.Type || value.Persist != task.Persist || value.Links.ParentTaskID != task.Links.ParentTaskID || !bytes.Equal(value.Inputs, task.Inputs) || !reflect.DeepEqual(value.Concurrency, task.Concurrency) {
			return task, errors.New("child identity already has different inputs")
		}
		return value, nil
	}
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
	delete(m.suspended, id)
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
	defer func() {
		cancel()
		m.wg.Wait()
	}()
	m.mu.Lock()
	m.active = map[string]context.CancelFunc{}
	m.suspended = map[string]bool{}
	m.mu.Unlock()
	if err := m.reconcile(lifetime); err != nil {
		return err
	}
	group, watchCtx := errgroup.WithContext(lifetime)
	group.Go(func() error {
		return m.Tasks.Watch(watchCtx, ">", func(_ *cluster.Record[domain.Task], _ bool) error {
			return m.schedule(watchCtx)
		})
	})
	group.Go(func() error {
		return m.Resources.Store.Watch(watchCtx, ">", func(_ *cluster.Record[map[string]string], _ bool) error {
			return m.schedule(watchCtx)
		})
	})
	return group.Wait()
}

var errSchedulerRead = errors.New("task scheduler read failed")

func (m *Manager) each(ctx context.Context, visit func(cluster.Record[domain.Task]) error) error {
	cursor := ""
	for {
		records, next, err := m.Tasks.List(ctx, cursor, 100)
		if err != nil {
			return errors.Join(errSchedulerRead, err)
		}
		for _, record := range records {
			owner, _, qualified := strings.Cut(record.Key, ".")
			if record.Value.ID != record.Key || (qualified && owner != record.Value.HostID) {
				// Subject-scoped write permission cannot authorize work on another host.
				continue
			}
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
			if record.Value.Persist {
				return m.persist(record.Value)
			}
			return m.finish(ctx, record, domain.Unknown, nil, errors.New("runner interrupted; inspect effects before retrying"))
		}
		if record.Value.Status.Terminal() {
			var journalErr error
			if record.Value.Persist {
				journalErr = m.Journal.Remove(record.Value)
			}
			return errors.Join(journalErr, m.Resources.Release(ctx, record.Key))
		}
		return nil
	})
}

// Retry transient reads without shutting down the peer or active runners.
// Writes with ambiguous outcomes still use the existing recovery path; this
// does not retry writes or task runners. Release the mutex between attempts.
func (m *Manager) schedule(ctx context.Context) error {
	warned := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := m.scheduleOnce(ctx)
		transient := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) || errors.Is(err, nats.ErrNoResponders) || errors.Is(err, jetstream.ErrNoStreamResponse)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, errSchedulerRead) || !transient {
			return err
		}
		if !warned {
			util.From(ctx).Warn("task scheduling waiting for JetStream", "error", util.Error(err))
			warned = true
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) scheduleOnce(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.each(ctx, func(record cluster.Record[domain.Task]) error {
		task := record.Value
		if task.HostID != m.HostID || m.active[task.ID] != nil || m.suspended[task.ID] {
			return nil
		}
		resuming := task.Status == domain.Running && task.Persist
		if task.Status == domain.Running && !resuming {
			return m.finish(ctx, record, domain.Unknown, nil, errors.New("running task has no local runner"))
		}
		if !resuming && task.Status != domain.Queued && !(task.Status == domain.ManualReview && task.Review != nil && task.Review.ApprovedAt != nil) {
			return nil
		}
		for _, id := range task.Links.AfterSuccessOfTaskIDs {
			dependency, err := m.Tasks.Get(ctx, id)
			if err != nil {
				return errors.Join(errSchedulerRead, err)
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
		if err = m.persist(task); err != nil {
			return m.finish(ctx, cluster.Record[domain.Task]{Key: task.ID, Value: task, Revision: revision}, domain.Failed, nil, err)
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
	if execution.record.Value.Persist && (lifetime.Err() != nil || (errors.Is(runErr, ErrRestart) && ctx.Err() == nil)) {
		m.suspended[record.Key] = true
		if err := m.persist(execution.record.Value); err != nil {
			util.From(ctx).Error("task restart persistence failed", "task", record.Key, "error", err)
		}
		return
	}
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
	var journalErr error
	if task.Persist {
		journalErr = m.Journal.Remove(task)
	}
	return errors.Join(journalErr, m.Resources.Release(ctx, record.Key))
}
