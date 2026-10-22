package cluster

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
)

func (m *Manager) Requests(state, limit, cursor string) (domain.Page[domain.JoinRequest], error) {
	page := domain.Page[domain.JoinRequest]{Items: []domain.JoinRequest{}}
	if _, err := domain.PageLimit(limit); err != nil {
		return page, err
	}
	if state != "" && state != "awaiting_approval" && state != "approved" && state != "rejected" && state != "expired" {
		return page, domain.Err("INVALID_QUERY", "Invalid enrollment state filter.", 400)
	}
	kv, _, err := m.enrollmentState()
	if err != nil {
		return page, err
	}
	keys, err := kv.Keys()
	if errors.Is(err, nats.ErrNoKeysFound) {
		return page, nil
	}
	if err != nil || len(keys) > domain.MaxRecords+domain.QueueCapacity {
		return page, unavailable()
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, key := range keys {
		if time.Now().After(deadline) {
			return page, unavailable()
		}
		if !strings.HasPrefix(key, "join.") {
			continue
		}
		record, _, err := readJoin(kv, strings.TrimPrefix(key, "join."))
		if err != nil {
			return page, unavailable()
		}
		view := publicJoin(record)
		if state == "" || state == view.State {
			page.Items = append(page.Items, view)
		}
	}
	sort.Slice(page.Items, func(i, j int) bool {
		if page.Items[i].ExpiresAt.Equal(page.Items[j].ExpiresAt) {
			return page.Items[i].ID > page.Items[j].ID
		}
		return page.Items[i].ExpiresAt.After(page.Items[j].ExpiresAt)
	})
	return domain.Paginate(page.Items, map[string]string{"collection": "join_requests", "state": state}, limit, cursor, func(r domain.JoinRequest) string { return r.ID })
}
func (m *Manager) JoinRequest(id string) (domain.JoinRequest, error) {
	if !domain.SafeID.MatchString(id) {
		return domain.JoinRequest{}, domain.Err("INVALID_QUERY", "Invalid request ID.", 400)
	}
	kv, _, err := m.enrollmentState()
	if err != nil {
		return domain.JoinRequest{}, err
	}
	record, _, err := readJoin(kv, id)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return domain.JoinRequest{}, domain.Err("NOT_FOUND", "Request not found.", 404)
	}
	if err != nil {
		return domain.JoinRequest{}, unavailable()
	}
	return publicJoin(record), nil
}
func (m *Manager) changed() {
	m.mu.RLock()
	s := m.service
	m.mu.RUnlock()
	if s != nil {
		s.NotifyChanges()
	}
}
