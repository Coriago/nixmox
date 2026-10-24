// Package task runs bounded, in-memory host tasks. JetStream is the queue;
// there is no task journal, checkpoint store or automatic execution recovery.
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
	"nixmox/internal/engine/telemetry"
)

var ErrBlocked = errors.New("task requires review")
var ErrResourceBusy = errors.New("host execution capacity or resource is busy")

const retainedTasks = 128

// Receipt is a bounded session hint, never a persisted execution record.
type Receipt struct {
	Hash    domain.Digest
	Receipt domain.Receipt
}

type Manager struct {
	mu   sync.Mutex
	life context.Context
	node domain.ID

	tasks             map[domain.ID]*execution
	resources         map[string]bool
	active            int
	workers           sync.WaitGroup
	closed            bool
	MembershipContext func(domain.Task) (context.Context, error)
	TaskChanged       func(context.Context, domain.Task)
	LogPublished      func(context.Context, domain.LogEvent)
	ProgressChanged   func(context.Context, domain.TaskProgress)
	AdapterFailures   atomic.Uint64
}

func NewManager(ctx context.Context, node domain.ID) *Manager {
	return &Manager{life: ctx, node: node, tasks: map[domain.ID]*execution{}, resources: map[string]bool{}}
}

func TaskID(command domain.ID) domain.ID { return shared.ID("task_", command) }

// RequestHash is an in-process conflict check, not a durable admission claim.
func RequestHash(command domain.Command) (domain.Digest, error) {
	raw, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&value); err != nil {
		return "", err
	}
	raw, err = json.Marshal(value)
	return shared.Fingerprint(raw), err
}

// Accept reserves capacity before acknowledging. Only a confirmed acknowledgement
// permits execution. A crash after acknowledgement may lose work; it never replays it.
func (m *Manager) Accept(ctx context.Context, envelope domain.Envelope, definition engine.TaskDefinition, acknowledge func(context.Context) error) (domain.Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Receipt{}, err
	}
	if m.closed || m.life.Err() != nil {
		return domain.Receipt{}, context.Canceled
	}
	hash, err := RequestHash(envelope.Command)
	if err != nil {
		return domain.Receipt{}, err
	}
	envelope.RequestHash = hash
	if e := m.tasks[TaskID(envelope.Command.ID)]; e != nil {
		prior := e.receipt()
		if prior.Hash != hash {
			return domain.Receipt{}, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Command ID already used in this daemon session"}
		}
		if err := acknowledge(ctx); err != nil {
			return domain.Receipt{}, err
		}
		return prior.Receipt, nil
	}
	if envelope.OwnerNodeID != m.node || envelope.Command.ID == "" {
		return domain.Receipt{}, errors.New("invalid executor identity")
	}
	if time.Now().After(envelope.ExpiresAt) {
		return domain.Receipt{}, &domain.Error{Code: "COMMAND_EXPIRED", Message: "Command expired in queue"}
	}
	if envelope.Command.Type == "task.cancel" || envelope.Command.Type == "task.retry" {
		return m.control(ctx, envelope, acknowledge)
	}
	return m.start(ctx, envelope, envelope, definition, nil, acknowledge)
}

// start and control run under m.mu; no execution is visible before its queue ack.
func (m *Manager) start(ctx context.Context, input, receiptEnvelope domain.Envelope, def engine.TaskDefinition, retryOf *domain.ID, ack func(context.Context) error) (domain.Receipt, error) {
	if def.Run == nil {
		return domain.Receipt{}, domain.Unavailable(input.Command.Type)
	}
	if m.active >= 8 {
		return domain.Receipt{}, ErrResourceBusy
	}
	for _, key := range def.Resources {
		if m.resources[key] {
			return domain.Receipt{}, ErrResourceBusy
		}
	}
	if err := m.trim(); err != nil {
		return domain.Receipt{}, err
	}
	now := time.Now().UTC()
	id := TaskID(receiptEnvelope.Command.ID)
	current := domain.Task{Record: domain.Record{SchemaVersion: 1, ID: id, Version: 1}, CommandID: receiptEnvelope.Command.ID, Kind: input.Command.Type, Scope: input.Scope, ClusterID: input.ClusterID, MembershipEpoch: input.MembershipEpoch, OwnerNodeID: m.node, TargetID: input.Command.TargetID, RetryOf: retryOf, State: domain.Running, Phase: def.Phase, Cancellation: domain.Cancellation{Allowed: def.Interruptible}, CreatedAt: now, UpdatedAt: now, Log: domain.LogReference{NodeID: m.node}}
	life, cancel := context.WithCancel(m.life)
	stop := func() bool { return false }
	if input.Scope == domain.ClusterScope && m.MembershipContext != nil {
		scope, err := m.MembershipContext(current)
		if err != nil {
			cancel()
			return domain.Receipt{}, err
		}
		stop = context.AfterFunc(scope, cancel)
	}
	if err := ack(ctx); err != nil {
		stop()
		cancel()
		return domain.Receipt{}, err
	}
	e := &execution{manager: m, current: current, hash: receiptEnvelope.RequestHash, envelope: input, definition: def, cancel: cancel, logs: map[domain.ID]*logBuffer{}, activities: map[domain.ID]domain.Activity{}}
	m.tasks[id] = e
	receipt := domain.Receipt{CommandID: receiptEnvelope.Command.ID, Admission: "accepted", TaskID: &id}
	for _, key := range def.Resources {
		m.resources[key] = true
	}
	m.active++
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		defer func() {
			stop()
			cancel()
			m.mu.Lock()
			for _, key := range def.Resources {
				delete(m.resources, key)
			}
			m.active--
			m.mu.Unlock()
		}()
		e.run(life)
	}()
	return receipt, nil
}

func (m *Manager) trim() error {
	if len(m.tasks) >= retainedTasks {
		var oldest *execution
		for _, e := range m.tasks {
			t := e.Task()
			if terminal(t.State) && (oldest == nil || t.UpdatedAt.Before(oldest.Task().UpdatedAt)) {
				oldest = e
			}
		}
		if oldest == nil {
			return ErrResourceBusy
		}
		id := oldest.Task().ID
		delete(m.tasks, id)
	}

	return nil
}

func (m *Manager) control(ctx context.Context, envelope domain.Envelope, ack func(context.Context) error) (domain.Receipt, error) {
	var p domain.TaskTarget
	if err := json.Unmarshal(envelope.Command.Payload, &p); err != nil {
		return domain.Receipt{}, err
	}
	e := m.tasks[p.TaskID]
	if e == nil {
		return domain.Receipt{}, &domain.Error{Code: "TASK_UNAVAILABLE", Message: "Task belongs to an unavailable executor session"}
	}
	old := e.Task()
	if envelope.Command.TargetID != p.TaskID || len(envelope.Command.ExpectedVersions) != 1 || envelope.Command.ExpectedVersions["task:"+string(p.TaskID)] != old.Version {
		return domain.Receipt{}, &domain.Error{Code: "VERSION_CONFLICT", Message: "Review the current task version"}
	}
	if old.Scope != envelope.Scope || old.MembershipEpoch != envelope.MembershipEpoch || (old.ClusterID == nil) != (envelope.ClusterID == nil) || (old.ClusterID != nil && *old.ClusterID != *envelope.ClusterID) {
		return domain.Receipt{}, &domain.Error{Code: "SCOPE_MISMATCH", Message: "Task control scope mismatch"}
	}
	if envelope.Command.Type == "task.retry" {
		if !terminal(old.State) || !e.definition.SafeRetry {
			return domain.Receipt{}, &domain.Error{Code: "CONFIRMATION_REQUIRED", Message: "Submit a fresh operation-specific request for this work"}
		}
		input := e.Envelope()
		input.Command.ID = envelope.Command.ID
		return m.start(ctx, input, envelope, e.definition, &old.ID, ack)
	}
	if err := m.trim(); err != nil {
		return domain.Receipt{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current.Version != old.Version {
		return domain.Receipt{}, &domain.Error{Code: "VERSION_CONFLICT", Message: "Task changed before cancellation"}
	}
	if !e.current.Cancellation.Allowed || terminal(e.current.State) {
		return domain.Receipt{}, &domain.Error{Code: "CANCELLATION_REFUSED", Message: "Current work cannot be interrupted"}
	}
	if err := ack(ctx); err != nil {
		return domain.Receipt{}, err
	}
	e.current.Cancellation.Allowed = false
	e.current.State, e.current.Cancellation.Requested = domain.Cancelling, true
	e.current.Version++
	e.current.UpdatedAt = time.Now().UTC()
	e.cancel()
	id, now := TaskID(envelope.Command.ID), time.Now().UTC()
	result, _ := json.Marshal(map[string]any{"task_id": p.TaskID, "cancel_requested": true})
	current := domain.Task{Record: domain.Record{SchemaVersion: 1, ID: id, Version: 1}, CommandID: envelope.Command.ID, Kind: "task.cancel", Scope: old.Scope, ClusterID: old.ClusterID, MembershipEpoch: old.MembershipEpoch, OwnerNodeID: m.node, TargetID: p.TaskID, State: domain.Succeeded, CreatedAt: now, UpdatedAt: now, Result: result}
	m.tasks[id] = &execution{manager: m, current: current, hash: envelope.RequestHash, envelope: envelope, cancel: func() {}}
	if m.TaskChanged != nil {
		m.TaskChanged(ctx, current)
	}
	receipt := domain.Receipt{CommandID: envelope.Command.ID, Admission: "accepted", TaskID: &id}
	return receipt, nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, e := range m.tasks {
		e.cancel()
	}
	m.mu.Unlock()
	m.workers.Wait()
}
func (m *Manager) Receipt(_ context.Context, id domain.ID) (Receipt, error) {
	e, err := m.find(TaskID(id))
	if err != nil {
		return Receipt{}, err
	}
	return e.receipt(), nil
}
func (e *execution) receipt() Receipt {
	t := e.Task()
	return Receipt{Hash: e.hash, Receipt: domain.Receipt{CommandID: t.CommandID, Admission: "accepted", TaskID: &t.ID}}
}
func (m *Manager) find(id domain.ID) (*execution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.tasks[id]
	if e == nil {
		return nil, os.ErrNotExist
	}
	return e, nil
}
func (m *Manager) Task(_ context.Context, id domain.ID) (domain.Task, error) {
	e, err := m.find(id)
	if err != nil {
		return domain.Task{}, err
	}
	return e.Task(), nil
}
func (m *Manager) Input(_ context.Context, id domain.ID) (domain.Envelope, error) {
	e, err := m.find(id)
	if err != nil {
		return domain.Envelope{}, err
	}
	return e.Envelope(), nil
}
func (m *Manager) List() []domain.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	tasks := make([]domain.Task, 0, len(m.tasks))
	for _, e := range m.tasks {
		tasks = append(tasks, e.Task())
	}
	return tasks
}
func terminal(state domain.TaskState) bool {
	return state == domain.Succeeded || state == domain.Failed || state == domain.Cancelled || state == domain.Blocked
}

type execution struct {
	manager    *Manager
	mu         sync.Mutex
	current    domain.Task
	envelope   domain.Envelope
	hash       domain.Digest
	cancel     context.CancelFunc
	ctx        context.Context
	definition engine.TaskDefinition
	progress   domain.TaskProgress
	activities map[domain.ID]domain.Activity
	logs       map[domain.ID]*logBuffer
}

func (e *execution) Task() domain.Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	return shared.Clone(e.current)
}
func (e *execution) Envelope() domain.Envelope { return shared.Clone(e.envelope) }
func (e *execution) update(change func(*domain.Task)) {
	e.mu.Lock()
	change(&e.current)
	e.current.Version++
	e.current.UpdatedAt = time.Now().UTC()
	current := shared.Clone(e.current)
	e.mu.Unlock()
	if e.manager.TaskChanged != nil {
		e.manager.TaskChanged(e.ctx, current)
	}
}
func (e *execution) run(ctx context.Context) {
	ctx, span := telemetry.Start(telemetry.Extract(ctx, e.envelope.Trace), "task.execute "+e.current.Kind)
	e.ctx = context.WithoutCancel(ctx)
	e.update(func(*domain.Task) {})
	result := ctx.Err()
	if result == nil {
		result = e.definition.Run(ctx, e)
	}
	defer func() { telemetry.End(span, result) }()
	e.update(func(t *domain.Task) {
		t.Cancellation.Allowed = false
		switch {
		case ctx.Err() != nil:
			t.State = domain.Cancelled
		case errors.Is(result, ErrBlocked):
			t.State = domain.Blocked
		case result != nil:
			t.State = domain.Failed
			if !errors.As(result, &t.Error) {
				t.Error = &domain.Error{Code: "TASK_FAILED", Message: "Task failed; inspect its output"}
			}
		default:
			t.State = domain.Succeeded
		}
	})
}

// Phase changes observation/cancellation policy, not control flow. The operation
// remains an ordinary Go function; a cancelled phase cannot start its successor.
func (e *execution) Phase(ctx context.Context, name string, interruptible bool) error {
	var err error
	e.update(func(t *domain.Task) {
		err = ctx.Err()
		if t.Cancellation.Requested {
			err = context.Canceled
		}
		if err == nil {
			t.Phase, t.Cancellation.Allowed = name, interruptible
		}
	})
	return err
}
func (e *execution) Result(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(result) > domain.CommandPayloadLimit {
		return errors.New("invalid task result")
	}
	e.update(func(t *domain.Task) { t.Result = result })
	return nil
}
func (e *execution) Block(_ context.Context, review domain.Review) error {
	e.update(func(t *domain.Task) { t.Review = &review })
	return ErrBlocked
}
func (e *execution) Log() io.Writer { return taskLogWriter{e} }
func (e *execution) Runner(base engine.Runner) engine.Runner {
	return taskRunner{execution: e, base: base}
}
func (e *execution) Progress(ctx context.Context, progress domain.TaskProgress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	if progress.TaskID != e.current.ID || progress.Version <= e.progress.Version {
		e.mu.Unlock()
		return errors.New("invalid progress identity/version")
	}
	e.progress = shared.Clone(progress)
	e.mu.Unlock()
	if e.manager.ProgressChanged != nil {
		e.manager.ProgressChanged(e.ctx, progress)
	}
	return nil
}

type taskLogWriter struct{ e *execution }

func (w taskLogWriter) Write(data []byte) (int, error) {
	id := w.e.Task().ID
	written := 0
	for len(data) > 0 {
		n := min(len(data), domain.LogEventLimit/2)
		if _, err := w.e.manager.Append(w.e.ctx, id, nil, data[:n]); err != nil {
			return written, err
		}
		data = data[n:]
		written += n
	}
	return written, nil
}
