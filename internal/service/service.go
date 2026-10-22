// Package service is the shared command admission and execution layer.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	SchemaVersion int            `json:"schema_version"`
	Fingerprint   string         `json:"fingerprint"`
	Command       domain.Command `json:"command"`
	Receipt       domain.Receipt `json:"receipt"`
	CreatedAt     time.Time      `json:"created_at"`
	Complete      bool           `json:"complete"`
	ParentID      *string        `json:"parent_id,omitempty"`
}
type Service struct {
	mu           sync.Mutex
	store        *storage.Store
	inspector    Inspector
	identity     domain.Identity
	admissions   map[string]admission
	tasks        map[string]domain.Task
	network      domain.NetworkStatus
	ctx          context.Context
	cancel       context.CancelFunc
	wake         chan struct{}
	done         chan struct{}
	activeCancel context.CancelFunc
	activeID     string
	fatal        bool
	subscribers  map[chan struct{}]struct{}
	// Crash-boundary injection is used only by storage recovery tests.
	checkpoint func(string) error
}

func Open(store *storage.Store, inspector Inspector) (*Service, error) {
	id, err := store.Identity()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{store: store, inspector: inspector, identity: id, admissions: map[string]admission{}, tasks: map[string]domain.Task{}, network: domain.UnknownNetwork(id.NodeID), ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), subscribers: map[chan struct{}]struct{}{}}
	if err := s.recover(); err != nil {
		cancel()
		return nil, err
	}
	go s.work()
	s.signal()
	return s, nil
}
func (s *Service) Close()                    { s.cancel(); <-s.done }
func (s *Service) Identity() domain.Identity { return s.identity }
func taskID(commandID string) string {
	h := sha256.Sum256([]byte(commandID))
	return "task_" + hex.EncodeToString(h[:])
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
		if t.SchemaVersion != 1 || n != t.ID+".json" || !domain.SafeID.MatchString(t.ID) || t.OwnerNodeID != s.identity.NodeID || t.Scope != "node_local" || t.Kind != "network.check" {
			return errors.New("unsupported task record")
		}
		switch t.State {
		case "queued", "running", "reconciling", "cancelling", "succeeded", "failed", "cancelled":
		default:
			return errors.New("invalid persisted task state")
		}
		s.tasks[t.ID] = t
		if t.State == "succeeded" && t.Result != nil && t.Result.ObservedAt != nil && (s.network.ObservedAt == nil || t.Result.ObservedAt.After(*s.network.ObservedAt)) {
			s.network = *t.Result
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
		if t.State == "running" {
			if err := t.Transition("reconciling", "inspect"); err != nil {
				return err
			}
			if err := s.persistTask(t); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) Admit(c domain.Command) (domain.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !domain.SafeID.MatchString(c.ID) || !domain.SafeID.MatchString(c.TargetID) || c.Type == "" {
		return domain.Receipt{}, domain.Err("INVALID_COMMAND", "id, type and target_id are required.", 422)
	}
	if c.TargetID != s.identity.NodeID {
		return domain.Receipt{}, domain.Err("TARGET_UNREACHABLE", "This unjoined daemon can address only its own node.", 503)
	}
	if len(c.Payload) == 0 || string(c.Payload) == "null" {
		return domain.Receipt{}, domain.Err("INVALID_PAYLOAD", "An object payload is required.", 422)
	}
	if len(c.Payload) > domain.MaxCommandBytes {
		return domain.Receipt{}, domain.Err("INVALID_PAYLOAD", "Payload exceeds 256 KiB.", 422)
	}
	var object map[string]json.RawMessage
	if err := domain.StrictJSON(c.Payload, &object); err != nil || object == nil {
		return domain.Receipt{}, domain.Err("INVALID_PAYLOAD", "Invalid object payload.", 422)
	}
	for _, v := range c.ExpectedVersions {
		if v <= 0 {
			return domain.Receipt{}, domain.Err("INVALID_COMMAND", "Expected versions must be positive.", 422)
		}
	}
	canonical, _ := json.Marshal(object)
	c.Payload = canonical
	if c.ExpectedVersions == nil {
		c.ExpectedVersions = map[string]int64{}
	}
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	fingerprint := hex.EncodeToString(h[:])
	if a, ok := s.admissions[c.ID]; ok {
		if a.Fingerprint != fingerprint {
			return domain.Receipt{}, domain.Err("IDEMPOTENCY_CONFLICT", "Command ID is already bound to another request.", 409)
		}
		if !a.Complete || s.fatal {
			return domain.Receipt{}, domain.Err("OUTCOME_UNKNOWN", "Look up this command after service recovery.", 503)
		}
		return a.Receipt, nil
	}
	if s.fatal || s.ctx.Err() != nil {
		return domain.Receipt{}, domain.Err("UNAVAILABLE", "Admission is unavailable.", 503)
	}
	a := admission{SchemaVersion: 1, Fingerprint: fingerprint, Command: c, CreatedAt: time.Now().UTC(), Receipt: domain.Receipt{CommandID: c.ID, Admission: "accepted", TaskID: taskID(c.ID)}}
	switch c.Type {
	case "network.check":
		if len(object) != 0 || len(c.ExpectedVersions) != 0 {
			return domain.Receipt{}, domain.Err("INVALID_PAYLOAD", "network.check accepts an empty payload and no expected versions.", 422)
		}
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
		if len(c.ExpectedVersions) != 1 || c.ExpectedVersions["task:"+t.ID] != t.Version {
			return domain.Receipt{}, domain.Err("VERSION_CONFLICT", "Task changed after this review.", 409)
		}
		if c.Type == "task.cancel" {
			if t.Terminal() {
				return domain.Receipt{}, domain.Err("INVALID_STATE", "Terminal tasks cannot be cancelled.", 409)
			}
			a.Receipt.TaskID = t.ID
		} else {
			if !t.Terminal() {
				return domain.Receipt{}, domain.Err("INVALID_STATE", "Only terminal tasks can be retried.", 409)
			}
			a.ParentID = &t.ID
		}
	default:
		// There is deliberately no local fallback for cluster mutations.
		if clusterOperation(c.Type) {
			return domain.Receipt{}, domain.Err("QUORUM_UNAVAILABLE", "Cluster admission requires enrollment and authenticated JetStream quorum.", 503)
		}
		return domain.Receipt{}, domain.Err("UNSUPPORTED_CAPABILITY", "Operation is not implemented on this node.", 503)
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
		if s.activeID == t.ID && s.activeCancel != nil {
			s.activeCancel()
		}
	} else if _, ok := s.tasks[a.Receipt.TaskID]; !ok {
		t := domain.Task{SchemaVersion: 1, ID: a.Receipt.TaskID, Version: 1, CommandID: a.Command.ID, Kind: "network.check", State: "queued", Phase: "inspect", Scope: "node_local", OwnerNodeID: s.identity.NodeID, CoordinatorID: s.identity.NodeID, TargetID: a.Command.TargetID, ParentID: a.ParentID, CreatedAt: a.CreatedAt, UpdatedAt: a.CreatedAt, Cancellation: domain.Cancellation{Allowed: true}, Effects: map[string]bool{}}
		t.Log.NodeID = s.identity.NodeID
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
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		}
		for {
			s.mu.Lock()
			if s.fatal || s.ctx.Err() != nil {
				s.mu.Unlock()
				break
			}
			var selected *domain.Task
			for _, t := range s.tasks {
				if t.Terminal() {
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
			if err := t.Transition("running", "inspect"); err != nil {
				s.fatal = true
				s.mu.Unlock()
				break
			}
			if err := s.persistTask(t); err != nil {
				s.mu.Unlock()
				break
			}
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			s.activeID = t.ID
			s.activeCancel = cancel
			s.notify()
			s.mu.Unlock()
			status, err := s.inspector.Inspect(ctx, s.identity.NodeID)
			cancel()
			s.mu.Lock()
			s.activeID = ""
			s.activeCancel = nil
			t = s.tasks[t.ID]
			if s.fatal || s.ctx.Err() != nil {
				s.mu.Unlock()
				break
			}
			if t.State == "cancelling" {
				_ = t.Transition("cancelled", "inspect")
			} else if err != nil {
				_ = t.Transition("failed", "inspect")
				t.Error = domain.Err("NETWORK_UNAVAILABLE", "Local network inspection failed.", 503)
			} else {
				_ = t.Transition("succeeded", "complete")
				t.Result = &status
			}
			if err := s.persistTask(t); err == nil && t.State == "succeeded" {
				s.network = status
			}
			s.notify()
			s.mu.Unlock()
		}
	}
}
func (s *Service) Receipt(id string) (domain.Receipt, error) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
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
