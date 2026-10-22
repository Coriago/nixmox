package cluster

import (
	"errors"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
	"nixmox/internal/service"
)

func (m *Manager) PrepareRemoteControl(c domain.Command, fingerprint, cluster string) (domain.PreparedCommand, error) {
	var payload struct {
		TaskID string `json:"task_id"`
	}
	if domain.StrictJSON(c.Payload, &payload) != nil || !domain.SafeID.MatchString(payload.TaskID) {
		return domain.PreparedCommand{}, domain.Err("INVALID_PAYLOAD", "A task_id is required.", 422)
	}
	task, err := m.RemoteTask(payload.TaskID)
	if err != nil {
		return domain.PreparedCommand{}, err
	}
	m.mu.RLock()
	d, s := m.dispatcher, m.service
	m.mu.RUnlock()
	if d == nil || s == nil || d.cluster != cluster {
		return domain.PreparedCommand{}, unavailable()
	}
	member, err := m.member(task.OwnerNodeID)
	if err != nil {
		return domain.PreparedCommand{}, err
	}
	if member.Epoch != task.MembershipEpoch {
		return domain.PreparedCommand{}, domain.Err("MEMBERSHIP_CHANGED", "Task belongs to an inactive membership epoch.", 409)
	}
	original, _, err := d.read(task.CommandID)
	if err != nil {
		return domain.PreparedCommand{}, err
	}
	op, ok := s.Operation(task.Kind)
	if !ok {
		return domain.PreparedCommand{}, domain.Err("UNSUPPORTED_CAPABILITY", "Task kind is unavailable on this coordinator.", 503)
	}
	return service.PrepareTaskControl(c, fingerprint, cluster, d.node, task, original.Prepared, op.SafeRetry)
}

func readTaskProjection(kv nats.KeyValue, id string) (domain.Task, error) {
	entry, err := kv.Get("task." + id)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return domain.Task{}, domain.Err("NOT_FOUND", "Task not found.", 404)
	}
	if err != nil {
		return domain.Task{}, unavailable()
	}
	var projection taskProjection
	if domain.StrictJSON(entry.Value(), &projection) != nil || projection.Task.ID != id {
		return domain.Task{}, unavailable()
	}
	if !projection.Task.Terminal() && (projection.ObservedAt.IsZero() || time.Since(projection.ObservedAt) > domain.ObservationLifetime) {
		return domain.Task{}, domain.Err("OWNER_UNAVAILABLE", "Current task state is unavailable from its owner.", 503)
	}
	return projection.Task, nil
}
func (m *Manager) RemoteTask(id string) (domain.Task, error) {
	if !domain.SafeID.MatchString(id) {
		return domain.Task{}, domain.Err("INVALID_QUERY", "Invalid task ID.", 400)
	}
	kv, _, err := m.enrollmentState()
	if err != nil {
		return domain.Task{}, err
	}
	return readTaskProjection(kv, id)
}
func (m *Manager) RemoteTasks() ([]domain.Task, error) {
	m.mu.RLock()
	if m.membership == nil || !m.membership.Provisional {
		m.mu.RUnlock()
		return nil, nil
	}
	if m.stores == nil {
		m.mu.RUnlock()
		return nil, unavailable()
	}
	kv, own := m.stores.State, m.identity.NodeID
	m.mu.RUnlock()
	keys, err := kv.Keys()
	if err != nil || len(keys) > domain.MaxRecords+domain.QueueCapacity {
		return nil, unavailable()
	}
	tasks := []domain.Task{}
	deadline := time.Now().Add(3 * time.Second)
	for _, key := range keys {
		if !strings.HasPrefix(key, "task.") {
			continue
		}
		if time.Now().After(deadline) {
			return nil, unavailable()
		}
		task, err := readTaskProjection(kv, strings.TrimPrefix(key, "task."))
		if err != nil {
			return nil, err
		}
		if task.OwnerNodeID != own {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}
