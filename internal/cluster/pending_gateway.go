package cluster

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
)

func pendingCommandAllowed(command domain.Command, node string) bool {
	if command.TargetID != node {
		return false
	}
	return command.Type == "task.cancel" || command.Type == "task.retry" || command.Type == "host.refresh"
}
func (m *Manager) peerRequest(node string, request queryRequest) statusReply {
	response := statusReply{}
	fail := func(err error) statusReply {
		var problem *domain.Error
		if !errors.As(err, &problem) {
			problem = unavailable()
		}
		return statusReply{Error: problem, ErrorStatus: problem.Status}
	}
	member, err := m.member(node)
	if err != nil {
		return fail(err)
	}
	if member.Epoch != request.Epoch {
		return fail(domain.Err("MEMBERSHIP_CHANGED", "Gateway request belongs to an inactive membership.", 409))
	}
	switch request.Kind {
	case "cluster.status":
		status, err := m.Status()
		if err != nil {
			return fail(err)
		}
		response.Status = &status
	case "command.submit":
		var command domain.Command
		if domain.StrictJSON(request.Payload, &command) != nil || !pendingCommandAllowed(command, node) {
			return fail(domain.Err("FORBIDDEN", "Pending gateways can control only their own assigned tasks and host refreshes.", 403))
		}
		m.mu.RLock()
		s := m.service
		m.mu.RUnlock()
		if s == nil {
			return fail(unavailable())
		}
		receipt, err := s.Admit(command)
		if err != nil {
			return fail(err)
		}
		response.Receipt = &receipt
	case "command.receipt":
		var payload struct {
			CommandID string `json:"command_id"`
		}
		if domain.StrictJSON(request.Payload, &payload) != nil || !domain.SafeID.MatchString(payload.CommandID) {
			return fail(domain.Err("INVALID_QUERY", "A command ID is required.", 400))
		}
		m.mu.RLock()
		d := m.dispatcher
		m.mu.RUnlock()
		if d == nil {
			return fail(unavailable())
		}
		record, _, err := d.read(payload.CommandID)
		if errors.Is(err, nats.ErrKeyNotFound) {
			return fail(domain.Err("NOT_FOUND", "Command not found.", 404))
		}
		if err != nil {
			return fail(err)
		}
		if record.Prepared.Command.TargetID != node {
			return fail(domain.Err("NOT_FOUND", "Command not found.", 404))
		}
		receipt, err := record.outcome()
		if err != nil {
			return fail(err)
		}
		response.Receipt = &receipt
	default:
		return fail(domain.Err("UNSUPPORTED_QUERY", "This query is not available to the node.", 422))
	}
	return response
}
func (m *Manager) pendingRequest(kind string, value any) (domain.Receipt, error) {
	m.mu.RLock()
	if m.closed || m.membership == nil || m.membership.Provisional || m.broker == nil {
		m.mu.RUnlock()
		return domain.Receipt{}, unavailable()
	}
	membership, nc := *m.membership, m.broker.Client
	m.mu.RUnlock()
	select {
	case m.querySlots <- struct{}{}:
		defer func() { <-m.querySlots }()
	default:
		return domain.Receipt{}, domain.Err("QUEUE_FULL", "Pending gateway request capacity is occupied.", 503)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return domain.Receipt{}, err
	}
	response, err := pendingQuery(nc, membership, queryRequest{Kind: kind, Payload: data}, 9*time.Second)
	if err != nil {
		return domain.Receipt{}, err
	}
	if response.Receipt == nil {
		return domain.Receipt{}, unavailable()
	}
	return *response.Receipt, nil
}
