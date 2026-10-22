// Package service is the shared command admission and execution layer.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/storage"
)

type Inspector interface {
	Inspect(context.Context, string) (domain.NetworkStatus, error)
}
type admission struct {
	SchemaVersion   int             `json:"schema_version"`
	Fingerprint     string          `json:"fingerprint"`
	Command         domain.Command  `json:"command"`
	Receipt         domain.Receipt  `json:"receipt"`
	CreatedAt       time.Time       `json:"created_at"`
	Complete        bool            `json:"complete"`
	ParentID        *string         `json:"parent_id,omitempty"`
	Kind            string          `json:"kind"`
	Scope           string          `json:"scope"`
	Resolved        json.RawMessage `json:"resolved"`
	Reconcile       bool            `json:"-"`
	Exclusions      []string        `json:"exclusions,omitempty"`
	ClusterID       *string         `json:"cluster_id,omitempty"`
	MembershipEpoch int             `json:"membership_epoch,omitempty"`
	CoordinatorID   string          `json:"coordinator_id,omitempty"`
}
type Service struct {
	sources              SourceReader
	configurations       ConfigurationReader
	hosts                HostReader
	deployments          DeploymentReader
	credentials          CredentialService
	cache                CacheService
	mu                   sync.Mutex
	store                *storage.Store
	inspector            Inspector
	operations           map[string]Operation
	identity             domain.Identity
	admissions           map[string]admission
	tasks                map[string]domain.Task
	network              domain.NetworkStatus
	ctx                  context.Context
	cancel               context.CancelFunc
	wake                 chan struct{}
	done                 chan struct{}
	active               map[string]context.CancelFunc
	workers              sync.WaitGroup
	clusterTransport     ClusterTransport
	fatal                bool
	subscribers          map[chan struct{}]struct{}
	telemetry            TelemetrySource
	telemetrySubscribers map[chan struct{}]struct{}
	// Crash-boundary injection is used only by storage recovery tests.
	checkpoint func(string) error
}

func Open(store *storage.Store, inspector Inspector, operations ...Operation) (*Service, error) {
	id, err := store.Identity()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{store: store, inspector: inspector, identity: id, admissions: map[string]admission{}, tasks: map[string]domain.Task{}, network: domain.UnknownNetwork(id.NodeID), ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), subscribers: map[chan struct{}]struct{}{}}
	s.operations = map[string]Operation{"network.check": networkOperation(inspector)}
	s.active = make(map[string]context.CancelFunc)
	for _, op := range operations {
		if _, exists := s.operations[op.Kind]; exists || op.Kind == "" || op.Validate == nil || op.Run == nil || op.Timeout <= 0 || (op.Scope != "node_local" && op.Scope != "cluster") {
			cancel()
			return nil, errors.New("invalid or duplicate operation registration")
		}
		s.operations[op.Kind] = op
	}
	if err := s.recover(); err != nil {
		cancel()
		return nil, err
	}
	go s.work()
	s.signal()
	return s, nil
}
func (s *Service) Close()                    { s.cancel(); <-s.done }
func (s *Service) Identity() domain.Identity { s.mu.Lock(); defer s.mu.Unlock(); return s.identity }
func taskID(commandID string) string {
	return domain.CommandTaskID(commandID)
}
func (s *Service) persistTask(t domain.Task) error {
	if err := s.store.Write("jobs/"+t.ID+".json", t); err != nil {
		s.fatal = true
		return err
	}
	s.tasks[t.ID] = t
	return nil
}
func (s *Service) saveAdmission(a admission) error {
	if err := s.store.Write("receipts/"+a.Command.ID+".json", a); err != nil {
		s.fatal = true
		return err
	}
	s.admissions[a.Command.ID] = a
	return nil
}
func (s *Service) boundary(name string) error {
	if s.checkpoint != nil {
		if err := s.checkpoint(name); err != nil {
			s.fatal = true
			return err
		}
	}
	return nil
}
func (s *Service) recover() error {
	names, err := s.store.Names("jobs")
	if err != nil {
		return err
	}
	if len(names) > domain.MaxRecords {
		return errors.New("task store capacity exceeded")
	}
	for _, n := range names {
		var t domain.Task
		if err := s.store.Read("jobs/"+n, &t); err != nil {
			return err
		}
		_, registered := s.operations[t.Kind]
		if t.SchemaVersion != 1 || n != t.ID+".json" || !domain.SafeID.MatchString(t.ID) || t.OwnerNodeID != s.identity.NodeID || (t.Scope != "node_local" && t.Scope != "cluster") || !registered {
			return errors.New("unsupported task record")
		}
		if t.Scope == "cluster" && t.ClusterID == nil {
			return errors.New("cluster task has no cluster context")
		}
		switch t.State {
		case "queued", "running", "blocked", "reconciling", "cancelling", "succeeded", "failed", "cancelled":
		default:
			return errors.New("invalid persisted task state")
		}
		s.tasks[t.ID] = t
		if result := t.NetworkResult(); t.State == "succeeded" && result != nil && result.ObservedAt != nil && (s.network.ObservedAt == nil || result.ObservedAt.After(*s.network.ObservedAt)) {
			s.network = *result
		}
	}
	names, err = s.store.Names("receipts")
	if err != nil {
		return err
	}
	if len(names) > domain.MaxRecords {
		return errors.New("receipt store capacity exceeded")
	}
	pending := []admission{}
	for _, n := range names {
		var a admission
		if err := s.store.Read("receipts/"+n, &a); err != nil {
			return err
		}
		if a.SchemaVersion != 1 || n != a.Command.ID+".json" || !domain.SafeID.MatchString(a.Command.ID) {
			return errors.New("unsupported receipt record")
		}
		s.admissions[a.Command.ID] = a
		if !a.Complete {
			pending = append(pending, a)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].CreatedAt.Before(pending[j].CreatedAt) })
	for _, a := range pending {
		if err := s.finishAdmission(a); err != nil {
			return err
		}
	}
	for _, t := range s.tasks {
		if _, ok := s.admissions[t.CommandID]; !ok {
			return errors.New("task has no admission record")
		}
		if !t.Terminal() && t.Scope == "cluster" && (s.identity.ClusterID == nil || *t.ClusterID != *s.identity.ClusterID || t.MembershipEpoch != s.identity.MembershipEpoch) && (t.State != "blocked" || t.Phase != "membership_fenced") {
			if t.State == "cancelling" || t.State == "blocked" {
				if err := t.Transition("reconciling", "membership_fenced"); err != nil {
					return err
				}
			}
			if err := t.Transition("blocked", "membership_fenced"); err != nil {
				return err
			}
			t.Cancellation.Allowed = false
			t.Error = domain.Err("MEMBERSHIP_CHANGED", "This task belongs to an inactive membership; its effects require reconciliation.", 409)
			if err := s.persistTask(t); err != nil {
				return err
			}
		}
		if t.State == "running" {
			if err := t.Transition("reconciling", t.Phase); err != nil {
				return err
			}
			if err := s.persistTask(t); err != nil {
				return err
			}
		}
		log, err := s.store.ReadLog(t.ID)
		if err != nil || log.End() < t.Log.NextOffset {
			return errors.New("task log is corrupt or missing")
		}
		if log.End() != t.Log.NextOffset {
			t = s.tasks[t.ID]
			t.Log.NextOffset = log.End()
			if err := s.persistTask(t); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) admit(c domain.Command, delivered *domain.PreparedCommand) (domain.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, fingerprint, err := domain.CanonicalCommand(c)
	if err != nil {
		return domain.Receipt{}, err
	}
	if a, ok := s.admissions[c.ID]; ok {
		if delivered != nil && (a.Scope != "cluster" || a.ClusterID == nil || *a.ClusterID != delivered.ClusterID) {
			return domain.Receipt{}, domain.Err("IDEMPOTENCY_CONFLICT", "Command ID belongs to another admission context.", 409)
		}
		if a.Fingerprint != fingerprint {
			return domain.Receipt{}, domain.Err("IDEMPOTENCY_CONFLICT", "Command ID is already bound to another request.", 409)
		}
		if !a.Complete || s.fatal {
			return domain.Receipt{}, domain.Err("OUTCOME_UNKNOWN", "Look up this command after service recovery.", 503)
		}
		return a.Receipt, nil
	}
	if delivered == nil && c.TargetID != s.identity.NodeID {
		return domain.Receipt{}, domain.Err("TARGET_UNREACHABLE", "This unjoined daemon can address only its own node.", 503)
	}
	if s.fatal || s.ctx.Err() != nil {
		return domain.Receipt{}, domain.Err("UNAVAILABLE", "Admission is unavailable.", 503)
	}
	if delivered != nil {
		if !json.Valid(delivered.Resolved) || len(delivered.Resolved) > domain.MaxCommandBytes || validateExclusions(delivered.Exclusions) != nil {
			return domain.Receipt{}, domain.Err("INVALID_COMMAND", "Invalid pinned command data.", 422)
		}
		if delivered.Fingerprint != fingerprint || s.identity.ClusterID == nil || *s.identity.ClusterID != delivered.ClusterID {
			return domain.Receipt{}, domain.Err("INVALID_COMMAND", "Invalid authenticated cluster command context.", 422)
		}
		if delivered.ExecutionNode() != s.identity.NodeID || delivered.ExecutionEpoch() != s.identity.MembershipEpoch || !domain.SafeID.MatchString(delivered.CoordinatorID) {
			return domain.Receipt{}, domain.Err("MEMBERSHIP_CHANGED", "Delivery belongs to another membership epoch.", 409)
		}
		if time.Now().After(delivered.ExpiresAt) {
			return domain.Receipt{}, domain.Err("COMMAND_EXPIRED", "Command expired before admission.", 409)
		}
	}
	a := admission{SchemaVersion: 1, Kind: c.Type, Scope: "node_local", Resolved: c.Payload, Fingerprint: fingerprint, Command: c, CreatedAt: time.Now().UTC(), Receipt: domain.Receipt{CommandID: c.ID, Admission: "accepted", TaskID: taskID(c.ID)}}
	if delivered != nil {
		a.ClusterID = &delivered.ClusterID
		a.MembershipEpoch = delivered.ExecutionEpoch()
		a.CoordinatorID = delivered.CoordinatorID
	}
	switch c.Type {
	case "task.cancel", "task.retry":
		var p struct {
			TaskID string `json:"task_id"`
		}
		if err := domain.StrictJSON(c.Payload, &p); err != nil || !domain.SafeID.MatchString(p.TaskID) {
			return domain.Receipt{}, domain.Err("INVALID_PAYLOAD", "A task_id is required.", 422)
		}
		t, ok := s.tasks[p.TaskID]
		if !ok {
			return domain.Receipt{}, domain.Err("NOT_FOUND", "Task not found.", 404)
		}
		if (delivered != nil && (t.Scope != "cluster" || delivered.Kind != t.Kind)) || (delivered == nil && t.Scope == "cluster") {
			return domain.Receipt{}, domain.Err("QUORUM_UNAVAILABLE", "Task controls must use their recorded admission scope.", 503)
		}
		if delivered != nil && t.MembershipEpoch != s.identity.MembershipEpoch {
			return domain.Receipt{}, domain.Err("MEMBERSHIP_CHANGED", "Task belongs to an inactive membership epoch.", 409)
		}
		if len(c.ExpectedVersions) != 1 || c.ExpectedVersions["task:"+t.ID] != t.Version {
			return domain.Receipt{}, domain.Err("VERSION_CONFLICT", "Task changed after this review.", 409)
		}
		if c.Type == "task.cancel" {
			if t.Terminal() || !t.Cancellation.Allowed {
				return domain.Receipt{}, domain.Err("INVALID_STATE", "Cancellation is unavailable in this phase.", 409)
			}
			a.Receipt.TaskID = t.ID
		} else {
			if !t.Terminal() {
				return domain.Receipt{}, domain.Err("INVALID_STATE", "Only terminal tasks can be retried.", 409)
			}
			if !s.operations[t.Kind].SafeRetry {
				return domain.Receipt{}, domain.Err("CONFIRMATION_REQUIRED", "This operation requires a fresh review before retry.", 409)
			}
			a.ParentID = &t.ID
			a.Resolved = s.admissions[t.CommandID].Resolved
			a.Exclusions = s.admissions[t.CommandID].Exclusions
		}
		a.Kind = t.Kind
		a.Scope = t.Scope
	default:
		op, registered := s.operations[c.Type]
		if registered {
			if err := op.Validate(c); err != nil {
				return domain.Receipt{}, err
			}
		}
		if delivered == nil && ((registered && op.Scope == "cluster") || (!registered && clusterOperation(c.Type))) {
			return domain.Receipt{}, domain.Err("QUORUM_UNAVAILABLE", "Cluster admission requires enrollment and authenticated JetStream quorum.", 503)
		}
		if !registered {
			return domain.Receipt{}, domain.Err("UNSUPPORTED_CAPABILITY", "Operation is not implemented on this node.", 503)
		}
		if delivered != nil && (op.Scope != "cluster" || delivered.Kind != c.Type) {
			return domain.Receipt{}, domain.Err("INVALID_COMMAND", "Operation scope does not permit cluster delivery.", 422)
		}
		if delivered != nil && c.TargetID != s.identity.NodeID && (!op.RunOnCoordinator || delivered.CoordinatorID != s.identity.NodeID) {
			return domain.Receipt{}, domain.Err("FORBIDDEN", "Operation cannot execute away from its target.", 403)
		}
		a.Scope = op.Scope
		if delivered != nil {
			a.Resolved = delivered.Resolved
			a.Exclusions = append([]string(nil), delivered.Exclusions...)
		}
		if op.Resolve != nil && delivered == nil {
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			resolved, err := op.Resolve(ctx, c)
			cancel()
			if err != nil {
				return domain.Receipt{}, err
			}
			if len(resolved) > domain.MaxCommandBytes || !json.Valid(resolved) {
				return domain.Receipt{}, domain.Err("INVALID_STATE", "Resolved request exceeds its encoding bounds.", 422)
			}
			a.Resolved = resolved
		}
		if op.Exclusions != nil && delivered == nil {
			keys, err := op.Exclusions(c, a.Resolved)
			if err != nil {
				return domain.Receipt{}, err
			}
			if err := validateExclusions(keys); err != nil {
				return domain.Receipt{}, err
			}
			a.Exclusions = append([]string(nil), keys...)
		}
	}
	active := 0
	for _, t := range s.tasks {
		if !t.Terminal() {
			active++
		}
	}
	if len(s.admissions) >= domain.MaxRecords || (c.Type != "task.cancel" && (active >= domain.QueueCapacity || len(s.tasks) >= domain.MaxRecords)) {
		return domain.Receipt{}, domain.Err("QUEUE_FULL", "Local command capacity is exhausted; nothing was admitted.", 503)
	}
	if err := s.saveAdmission(a); err != nil {
		return domain.Receipt{}, unknown()
	}
	if err := s.boundary("intent"); err != nil {
		return domain.Receipt{}, unknown()
	}
	if err := s.finishAdmission(a); err != nil {
		return domain.Receipt{}, unknown()
	}
	s.signal()
	s.notify()
	return a.Receipt, nil
}
func unknown() error {
	return domain.Err("OUTCOME_UNKNOWN", "Durability is uncertain; query the same command ID after recovery.", 503)
}
func clusterOperation(kind string) bool {
	for _, prefix := range []string{"deployment.", "repository.", "configuration.", "host.", "credential.", "media.", "artifact.", "cache.", "onboarding."} {
		if len(kind) > len(prefix) && kind[:len(prefix)] == prefix {
			return true
		}
	}
	return kind == "cluster.approve" || kind == "cluster.reject"
}
func (s *Service) finishAdmission(a admission) error {
	if a.Command.Type == "task.cancel" {
		t, ok := s.tasks[a.Receipt.TaskID]
		if !ok {
			return errors.New("cancel target missing")
		}
		// Admission is serialized with completion. An incomplete cancel intent is replayed
		// before workers restart, so it cannot overwrite a terminal task.
		if !t.Terminal() && t.State != "cancelling" {
			t.Cancellation.Requested = true
			if err := t.Transition("cancelling", t.Phase); err != nil {
				return err
			}
			if err := s.persistTask(t); err != nil {
				return err
			}
		}
		if cancel := s.active[t.ID]; cancel != nil {
			cancel()
		}
	} else if _, ok := s.tasks[a.Receipt.TaskID]; !ok {
		t := domain.Task{SchemaVersion: 1, ID: a.Receipt.TaskID, Version: 1, CommandID: a.Command.ID, Kind: a.Kind, State: "queued", Phase: "queued", Scope: a.Scope, ClusterID: a.ClusterID, OwnerNodeID: s.identity.NodeID, CoordinatorID: s.identity.NodeID, TargetID: a.Command.TargetID, ParentID: a.ParentID, CreatedAt: a.CreatedAt, UpdatedAt: a.CreatedAt, Cancellation: domain.Cancellation{Allowed: true}, Effects: map[string]bool{}}
		t.Log.NodeID = s.identity.NodeID
		if a.Scope == "cluster" {
			t.CoordinatorID = a.CoordinatorID
			t.MembershipEpoch = a.MembershipEpoch
		}
		if err := s.persistTask(t); err != nil {
			return err
		}
	}
	if err := s.boundary("task"); err != nil {
		return err
	}
	a.Complete = true
	if err := s.saveAdmission(a); err != nil {
		return err
	}
	return s.boundary("receipt")
}
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) work() {
	defer close(s.done)
	defer s.workers.Wait()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		for {
			s.mu.Lock()
			if s.fatal || s.ctx.Err() != nil || len(s.active) >= 4 {
				s.mu.Unlock()
				break
			}
			var selected *domain.Task
			for _, t := range s.tasks {
				if t.State == "blocked" && t.RecheckAt != nil && !time.Now().Before(*t.RecheckAt) {
					_ = t.Transition("reconciling", t.Phase)
					t.RecheckAt = nil
					t.Error = nil
					if err := s.persistTask(t); err != nil {
						break
					}
				}
				if t.State == "blocked" && t.Error != nil && t.Error.Code == "RESOURCE_BUSY" && len(t.WaitingFor) > 0 && s.exclusionsAvailable(t.ID, t.WaitingFor) {
					_ = t.Transition("reconciling", t.Phase)
					t.Error = nil
					if s.persistTask(t) != nil {
						break
					}
				}
				if t.Terminal() || t.State == "blocked" || s.active[t.ID] != nil || (t.State == "queued" && !s.exclusionsAvailable(t.ID, s.admissions[t.CommandID].Exclusions)) {
					continue
				}
				if selected == nil || t.CreatedAt.Before(selected.CreatedAt) || (t.CreatedAt.Equal(selected.CreatedAt) && t.ID < selected.ID) {
					v := t
					selected = &v
				}
			}
			if selected == nil {
				s.mu.Unlock()
				break
			}
			t := *selected
			if t.State == "cancelling" {
				_ = t.Transition("cancelled", "inspect")
				_ = s.persistTask(t)
				s.notify()
				s.mu.Unlock()
				continue
			}
			a := s.admissions[t.CommandID]
			a.Reconcile = t.State == "reconciling"
			if t.State == "queued" {
				t.Exclusions = append([]string(nil), a.Exclusions...)
			}
			if err := t.Transition("running", t.Phase); err != nil {
				s.fatal = true
				s.mu.Unlock()
				break
			}
			if err := s.persistTask(t); err != nil {
				s.mu.Unlock()
				break
			}
			ctx, cancel := context.WithTimeout(s.ctx, s.operations[t.Kind].Timeout)
			s.active[t.ID] = cancel
			s.workers.Add(1)
			s.notify()
			s.mu.Unlock()
			go s.execute(ctx, cancel, t, a)
		}
	}
}
func (s *Service) execute(ctx context.Context, cancel context.CancelFunc, t domain.Task, a admission) {
	defer s.workers.Done()
	defer s.signal()
	result, err := s.runOperation(ctx, t, a)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, t.ID)
	t = s.tasks[t.ID]
	if s.fatal || s.ctx.Err() != nil {
		return
	}
	if t.State == "cancelling" {
		_ = t.Transition("cancelled", "inspect")
	} else if err != nil {
		var blocked *Blocked
		if errors.As(err, &blocked) {
			_ = t.Transition("blocked", blocked.Phase)
			t.Error = blocked.Reason
			if blocked.RecheckAfter >= time.Second && blocked.RecheckAfter <= time.Minute {
				at := time.Now().UTC().Add(blocked.RecheckAfter)
				t.RecheckAt = &at
			}
		} else {
			_ = t.Transition("failed", t.Phase)
			t.Error = safeOperationError(err)
		}
	} else {
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil || len(encoded) > domain.MaxCommandBytes {
			_ = t.Transition("failed", t.Phase)
			t.Error = domain.Err("RESULT_UNAVAILABLE", "Operation result exceeds its encoding bounds.", 500)
		} else {
			_ = t.Transition("succeeded", "complete")
			t.Result = encoded
			t.Error = nil
		}
	}
	if err := s.persistTask(t); err == nil && t.State == "succeeded" {
		if result := t.NetworkResult(); result != nil {
			s.network = *result
		}
	}
	s.notify()
}
func (s *Service) LocalReceipt(id string) (domain.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatal {
		return domain.Receipt{}, unknown()
	}
	a, ok := s.admissions[id]
	if !ok {
		return domain.Receipt{}, domain.Err("NOT_FOUND", "Command not found.", 404)
	}
	if !a.Complete {
		return domain.Receipt{}, unknown()
	}
	return a.Receipt, nil
}
func (s *Service) Task(id string) (domain.Task, error) {
	t, err := s.LocalTask(id)
	if err == nil {
		return t, nil
	}
	s.mu.Lock()
	transport := s.clusterTransport
	s.mu.Unlock()
	if reader, ok := transport.(RemoteTaskReader); ok {
		return reader.RemoteTask(id)
	}
	return t, err
}
func (s *Service) LocalTask(id string) (domain.Task, error) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	s.mu.Unlock()
	if !ok {
		return t, domain.Err("NOT_FOUND", "Task not found.", 404)
	}
	return t, nil
}
func (s *Service) Tasks() []domain.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}
func (s *Service) Network() domain.NetworkStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.network
	if n.ObservedAt != nil && time.Since(*n.ObservedAt) > domain.ObservationLifetime {
		n.Freshness = "stale"
	}
	return n
}
func (s *Service) Subscribe() (<-chan struct{}, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subscribers) >= 64 {
		return nil, nil, domain.Err("UNAVAILABLE", "Event connection limit reached.", 503)
	}
	ch := make(chan struct{}, 1)
	s.subscribers[ch] = struct{}{}
	return ch, func() { s.mu.Lock(); delete(s.subscribers, ch); s.mu.Unlock() }, nil
}
func (s *Service) notify() {
	for ch := range s.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (s *Service) NotifyChanges() { s.mu.Lock(); defer s.mu.Unlock(); s.notify() }
