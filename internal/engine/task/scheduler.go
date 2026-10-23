// Package task implements durable phase execution, process ownership and generic log adapters.
package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

var ErrBlocked = errors.New("task blocked for review")

var ErrResourceBusy = errors.New("resource already owned")

type Locks struct {
	mu       sync.Mutex
	owners   map[string]map[domain.ID]bool
	capacity map[string]int
	next     uint64
}

func NewLocks() *Locks {
	return &Locks{owners: map[string]map[domain.ID]bool{}, capacity: map[string]int{}}
}

func (l *Locks) Acquire(ctx context.Context, owner domain.ID, resources []engine.Resource) (engine.Fence, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range resources {
		if r.Key == "" || r.Capacity < 1 || seen[r.Key] {
			return nil, errors.New("invalid resource set")
		}
		seen[r.Key] = true
		if old, ok := l.capacity[r.Key]; ok && old != r.Capacity {
			return nil, errors.New("resource capacity changed")
		}
		if !l.owners[r.Key][owner] && len(l.owners[r.Key]) >= r.Capacity {
			return nil, ErrResourceBusy
		}
	}
	for _, r := range resources {
		if l.owners[r.Key] == nil {
			l.owners[r.Key] = map[domain.ID]bool{}
		}
		l.capacity[r.Key] = r.Capacity
		l.owners[r.Key][owner] = true
	}
	l.next++
	return &localFence{locks: l, owner: owner, resources: resources, token: l.next}, nil
}

type localFence struct {
	locks     *Locks
	owner     domain.ID
	resources []engine.Resource
	token     uint64
	once      sync.Once
}

func (f *localFence) Token() uint64 { return f.token }

func (f *localFence) Release(context.Context) error {
	f.once.Do(func() {
		f.locks.mu.Lock()
		defer f.locks.mu.Unlock()
		for _, r := range f.resources {
			delete(f.locks.owners[r.Key], f.owner)
		}
	})
	return nil
}

type Scheduler struct {
	MembershipContext func(domain.Task) (context.Context, error)
	AdapterFailures   atomic.Uint64
	Store             *Journal
	Definitions       map[string]engine.TaskDefinition
	Node              domain.ID
	Locks             *Locks
	mu                sync.Mutex
	active            map[domain.ID]*execution
}

func NewScheduler(store *Journal, node domain.ID, definitions []engine.TaskDefinition) (*Scheduler, error) {
	s := &Scheduler{Store: store, Node: node, Locks: NewLocks(), Definitions: map[string]engine.TaskDefinition{}, active: map[domain.ID]*execution{}}
	for _, def := range definitions {
		if def.Kind == "" || len(def.Phases) == 0 {
			return nil, errors.New("empty task definition")
		}
		if _, ok := s.Definitions[def.Kind]; ok {
			return nil, errors.New("duplicate task kind")
		}
		names := map[string]bool{}
		for _, p := range def.Phases {
			if p.Name == "" || p.Run == nil || names[p.Name] {
				return nil, errors.New("invalid phase definition")
			}
			if err := validateAdapters(append(append([]engine.LogAdapterFactory(nil), def.Adapters...), p.Adapters...)); err != nil {
				return nil, err
			}
			names[p.Name] = true
		}
		s.Definitions[def.Kind] = def
	}
	return s, nil
}

// RequestHash binds both local receipts and cluster submission claims to one
// canonical command, independent of JSON object ordering.
func RequestHash(c domain.Command) (domain.Digest, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return shared.Fingerprint(canonical), nil
}

func (s *Scheduler) Admit(ctx context.Context, e domain.Envelope) (domain.Receipt, error) {
	if e.Scope != domain.NodeLocal {
		return domain.Receipt{}, errors.New("cluster admission requires a durable delivery")
	}
	return s.admit(ctx, e)
}

// AdmitAssigned is used by a scoped leaf relay. The full service acknowledges
// JetStream only after it receives this executor's persisted admission outcome.
func (s *Scheduler) AdmitAssigned(ctx context.Context, e domain.Envelope) (domain.Receipt, error) {
	if e.Scope != domain.ClusterScope || e.ClusterID == nil {
		return domain.Receipt{}, errors.New("invalid cluster assignment")
	}
	return s.admit(ctx, e)
}

func (s *Scheduler) AdmitDelivery(ctx context.Context, delivery engine.Delivery) (domain.Receipt, error) {
	e := delivery.Envelope()
	if e.Scope != domain.ClusterScope || e.ClusterID == nil {
		return domain.Receipt{}, errors.New("invalid cluster delivery")
	}
	receipt, err := s.admit(ctx, e)
	if err != nil {
		return receipt, err
	}
	return receipt, delivery.Ack(ctx)
}

func (s *Scheduler) admit(ctx context.Context, e domain.Envelope) (domain.Receipt, error) {
	if e.OwnerNodeID != s.Node || e.Command.ID == "" || (e.Scope == domain.NodeLocal && e.ClusterID != nil) {
		return domain.Receipt{}, errors.New("invalid task ownership/scope")
	}
	if _, ok := s.Definitions[e.Command.Type]; !ok {
		return domain.Receipt{}, domain.Unavailable(e.Command.Type)
	}
	hash, err := RequestHash(e.Command)
	if err != nil {
		return domain.Receipt{}, err
	}
	e.RequestHash = hash
	prior, err := s.Store.Receipt(ctx, e.Command.ID)
	if err == nil {
		if prior.Envelope.RequestHash != hash {
			return domain.Receipt{}, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Command ID already used"}
		}
		if prior.Error != nil {
			return prior.Receipt, prior.Error
		}
		// Finish a crash-interrupted admission without allocating new identities.
		s.Store.mu.Lock()
		file, readErr := s.Store.admission(e.Command.ID)
		if readErr == nil {
			readErr = s.Store.ensureTask(file)
		}
		s.Store.mu.Unlock()
		return prior.Receipt, readErr
	}
	if !errors.Is(err, os.ErrNotExist) {
		return domain.Receipt{}, err
	}
	if e.ExpiresAt.IsZero() || time.Now().After(e.ExpiresAt) {
		return s.Store.Reject(ctx, e, &domain.Error{Code: "COMMAND_EXPIRED", Message: "Command expired before admission"})
	}
	now := time.Now().UTC()
	id := TaskID(e.Command.ID)
	task := domain.Task{MembershipEpoch: e.MembershipEpoch, Record: domain.Record{SchemaVersion: 1, ID: id, Version: 1}, CommandID: e.Command.ID, Kind: e.Command.Type, Scope: e.Scope, OwnerNodeID: s.Node, ClusterID: e.ClusterID, State: domain.Queued, TargetID: e.Command.TargetID, CreatedAt: now, UpdatedAt: now, Cancellation: domain.Cancellation{Allowed: true}, Effects: map[string]bool{}, Log: domain.LogReference{NodeID: s.Node}}
	receipt := domain.Receipt{CommandID: e.Command.ID, Admission: "accepted", TaskID: &id}
	record, err := s.Store.Admit(ctx, domain.AdmissionRecord{SchemaVersion: 1, Envelope: e, Receipt: receipt}, task)
	return record.Receipt, err
}

// Execute may run concurrently for distinct tasks; exclusions are per phase and
// retained on uncertain outcomes. A single journal process owns all local effects.
func (s *Scheduler) Execute(ctx context.Context, id domain.ID) error {
	s.mu.Lock()
	if _, ok := s.active[id]; ok {
		s.mu.Unlock()
		return ErrResourceBusy
	}
	task, err := s.Store.Task(ctx, id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if task.State != domain.Queued && task.State != domain.Reconciling {
		s.mu.Unlock()
		return errors.New("task is not eligible for execution")
	}
	life, cancel := context.WithCancel(ctx)
	var stopScope func() bool
	if task.Scope == domain.ClusterScope && s.MembershipContext != nil {
		scope, err := s.MembershipContext(task)
		if err != nil {
			s.mu.Unlock()
			cancel()
			return err
		}
		stopScope = context.AfterFunc(scope, cancel)
	}
	if stopScope != nil {
		defer stopScope()
	}
	e := &execution{scheduler: s, current: task, cancel: cancel}
	s.active[id] = e
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.active, id); s.mu.Unlock() }()
	def, ok := s.Definitions[task.Kind]
	if !ok {
		return domain.Unavailable(task.Kind)
	}
	for _, phase := range def.Phases {
		current := e.Task()
		checkpoint := findCheckpoint(current, phase.Name)
		if checkpoint != nil && checkpoint.State == "observed" {
			continue
		}
		fence, err := s.Locks.Acquire(life, id, phase.Resources)
		if err != nil {
			return err
		}
		recovering := checkpoint != nil && checkpoint.State == "intent"
		if recovering && phase.Reconcile == nil {
			return domain.Unavailable("phase reconciliation")
		}
		if err = e.update(func(t *domain.Task) {
			t.State = domain.Running
			t.Phase = phase.Name
			t.Cancellation.Allowed = phase.Interruptible
			if !phase.Interruptible {
				reason := "Phase cannot be interrupted"
				t.Cancellation.Reason = &reason
			} else {
				t.Cancellation.Reason = nil
			}
		}); err != nil {
			return err
		}
		if !recovering {
			err = e.Intent(life, domain.Checkpoint{Phase: phase.Name, State: "intent", IntentAt: time.Now().UTC()})
		}
		if err == nil {
			e.mu.Lock()
			e.adapters = append(append([]engine.LogAdapterFactory(nil), def.Adapters...), phase.Adapters...)
			e.mu.Unlock()
			if recovering {
				err = phase.Reconcile(life, e)
			} else {
				err = phase.Run(life, e)
			}
		}
		current = e.Task()
		checkpoint = findCheckpoint(current, phase.Name)
		if errors.Is(err, ErrBlocked) {
			_ = fence.Release(context.Background())
			return err
		}
		if checkpoint == nil || checkpoint.State != "observed" {
			_ = e.update(func(t *domain.Task) {
				t.State = domain.Reconciling
				t.Cancellation.Allowed = false
				t.Error = &domain.Error{Code: "RECONCILIATION_REQUIRED", Message: "Phase effect requires executor observation"}
			})
			return errors.Join(err, errors.New("phase was not observed"))
		}
		_ = fence.Release(context.Background())
		if err != nil {
			state := domain.Failed
			if life.Err() != nil {
				state = domain.Cancelled
			}
			if e.Task().State == domain.Cancelling && state != domain.Cancelled {
				state = domain.Cancelled
			}
			if saveErr := e.update(func(t *domain.Task) {
				t.State = state
				t.Error = &domain.Error{Code: "TASK_FAILED", Message: "Phase failed; see sanitized task logs"}
			}); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			s.Store.Close(context.Background(), id, nil)
			return err
		}
	}
	if err = e.update(func(t *domain.Task) {
		if t.State == domain.Cancelling {
			t.State = domain.Cancelled
		} else {
			t.State = domain.Succeeded
		}
		t.Cancellation.Allowed = false
		t.Error = nil
	}); err != nil {
		return err
	}
	return s.Store.Close(context.Background(), id, nil)
}

// Run drains the durable journal with one bounded worker. Admission never
// depends on an in-memory queue, so restart cannot lose queued work.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		cursor := ""
		for {
			page, err := s.Store.Recoverable(ctx, domain.PageRequest{Cursor: cursor, Limit: domain.PageLimit})
			if err != nil {
				return err
			}
			for _, current := range page.Items {
				if _, ok := s.Definitions[current.Kind]; !ok {
					return domain.Unavailable("recovery handler for " + current.Kind)
				}
				if current.State != domain.Queued && current.State != domain.Reconciling {
					continue
				}
				if err = s.Execute(ctx, current.ID); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					// Failed/reconciling tasks remain queryable. Persistence failures
					// that leave an executing task running stop the worker.
					after, readErr := s.Store.Task(ctx, current.ID)
					if readErr != nil || after.State == domain.Running {
						return errors.Join(err, readErr)
					}
				}
			}
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func findCheckpoint(t domain.Task, name string) *domain.Checkpoint {
	for i := range t.Checkpoints {
		if t.Checkpoints[i].Phase == name {
			return &t.Checkpoints[i]
		}
	}
	return nil
}

func (s *Scheduler) Recover(ctx context.Context) error {
	cursor := ""
	for {
		page, err := s.Store.Recoverable(ctx, domain.PageRequest{Cursor: cursor, Limit: domain.PageLimit})
		if err != nil {
			return err
		}
		for _, task := range page.Items {
			if task.State == domain.Running || task.State == domain.Cancelling {
				old := task.Version
				task.Version++
				task.State = domain.Reconciling
				if err = s.Store.SaveTask(ctx, task, old); err != nil {
					return err
				}
			}
			// Reacquire uncertain exclusions before allowing ANY queued work to start.
			for _, phase := range s.Definitions[task.Kind].Phases {
				if cp := findCheckpoint(task, phase.Name); cp != nil && cp.State == "intent" {
					if _, err = s.Locks.Acquire(ctx, task.ID, phase.Resources); err != nil {
						return err
					}
				}
			}
		}
		if page.NextCursor == nil {
			return nil
		}
		cursor = *page.NextCursor
	}
}

func (s *Scheduler) Cancel(ctx context.Context, id domain.ID, version domain.Version) error {
	s.mu.Lock()
	active := s.active[id]
	s.mu.Unlock()
	if active != nil {
		active.mu.Lock()
		defer active.mu.Unlock()
		task := active.current
		if task.Version != version {
			return &domain.Error{Code: "VERSION_CONFLICT", Message: "Task changed"}
		}
		if !task.Cancellation.Allowed {
			return errors.New("current phase is not interruptible")
		}
		task.State = domain.Cancelling
		task.Cancellation.Requested = true
		task.Version++
		if err := s.Store.SaveTask(ctx, task, version); err != nil {
			return err
		}
		active.current = task
		active.cancel()
		return nil
	}
	task, err := s.Store.Task(ctx, id)
	if err != nil {
		return err
	}
	if task.State != domain.Queued && task.State != domain.Blocked {
		return errors.New("task cannot safely be cancelled")
	}
	if task.Version != version {
		return &domain.Error{Code: "VERSION_CONFLICT", Message: "Task changed"}
	}
	task.State = domain.Cancelling
	task.Cancellation.Requested = true
	task.Version++
	if err = s.Store.SaveTask(ctx, task, version); err != nil {
		return err
	}
	version = task.Version
	task.Version++
	task.State = domain.Cancelled
	if err = s.Store.SaveTask(ctx, task, version); err != nil {
		return err
	}
	return s.Store.Close(ctx, id, nil)
}

type execution struct {
	mu        sync.Mutex
	scheduler *Scheduler
	current   domain.Task
	cancel    context.CancelFunc
	adapters  []engine.LogAdapterFactory
}

func (e *execution) Task() domain.Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	return shared.Clone(e.current)
}

func (e *execution) update(change func(*domain.Task)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	next := shared.Clone(e.current)
	change(&next)
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	// Persist cancellation/evidence even if the execution context was cancelled.
	if err := e.scheduler.Store.SaveTask(context.Background(), next, e.current.Version); err != nil {
		return err
	}
	e.current = next
	return nil
}

func (e *execution) Intent(_ context.Context, cp domain.Checkpoint) error {
	return e.update(func(t *domain.Task) {
		if prior := findCheckpoint(*t, cp.Phase); prior == nil {
			cp.State = "intent"
			t.Checkpoints = append(t.Checkpoints, cp)
		} else if prior.State == "intent" && len(prior.Evidence) == 0 {
			// Refine the scheduler's generic intent before invoking an effect.
			// Existing effect evidence is immutable on retry/reconciliation.
			prior.Evidence = cp.Evidence
		}
	})
}

func (e *execution) Observed(_ context.Context, cp domain.Checkpoint) error {
	return e.update(func(t *domain.Task) {
		for i := range t.Checkpoints {
			if t.Checkpoints[i].Phase == cp.Phase {
				now := time.Now().UTC()
				t.Checkpoints[i].State = "observed"
				t.Checkpoints[i].ObservedAt = &now
				t.Checkpoints[i].Evidence = cp.Evidence
			}
		}
	})
}

func (e *execution) Block(_ context.Context, review domain.Review) error {
	if err := e.update(func(t *domain.Task) {
		t.State = domain.Blocked
		t.Review = &review
		if cp := findCheckpoint(*t, t.Phase); cp != nil {
			for i := range t.Checkpoints {
				if t.Checkpoints[i].Phase == t.Phase {
					t.Checkpoints[i].State = "paused"
				}
			}
		}
	}); err != nil {
		return err
	}
	return ErrBlocked
}

func (e *execution) Log() io.Writer { return taskLogWriter{e} }

func (e *execution) Runner(base engine.Runner) engine.Runner {
	return taskRunner{execution: e, base: base}
}

type taskLogWriter struct{ e *execution }

func (w taskLogWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		n := min(len(data), domain.LogEventLimit/2)
		if _, err := w.e.scheduler.Store.Append(context.Background(), w.e.Task().ID, nil, data[:n]); err != nil {
			return written, err
		}
		written += n
		data = data[n:]
	}
	return written, nil
}

func (e *execution) Progress(ctx context.Context, p domain.TaskProgress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	task := e.Task()
	if p.TaskID != task.ID {
		return errors.New("progress task mismatch")
	}
	j := e.scheduler.Store
	j.mu.Lock()
	defer j.mu.Unlock()
	var prior domain.TaskProgress
	path := "jobs/" + shared.Filename(task.ID) + "/progress.json"
	if err := j.read(path, &prior); err == nil && prior.Version >= p.Version {
		return errors.New("stale progress version")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return j.write(path, p)
}
