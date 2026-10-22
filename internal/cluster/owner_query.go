package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
	"nixmox/internal/service"
	"nixmox/internal/storage"
)

const queryChunk = 32 << 10

type ownerRequest struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	TaskID   string    `json:"task_id"`
	Epoch    int       `json:"membership_epoch"`
	Offset   int       `json:"offset"`
	Limit    int       `json:"limit"`
	Deadline time.Time `json:"deadline"`
}
type ownerReply struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id"`
	Epoch       int             `json:"membership_epoch"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       *domain.Error   `json:"error,omitempty"`
	ErrorStatus int             `json:"error_status,omitempty"`
}
type logBounds struct {
	Start    int  `json:"start"`
	End      int  `json:"end"`
	Complete bool `json:"complete"`
}

func (a *executorAgent) queries() {
	defer a.workers.Done()
	for a.ctx.Err() == nil {
		msg, err := a.querySub.NextMsg(250 * time.Millisecond)
		if err != nil {
			if !errors.Is(err, nats.ErrTimeout) {
				select {
				case <-a.ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
			continue
		}
		var request ownerRequest
		if len(msg.Data) > 4096 || domain.StrictJSON(msg.Data, &request) != nil || !domain.SafeID.MatchString(request.ID) || !domain.SafeID.MatchString(request.TaskID) || request.Epoch != a.membership.Epoch || time.Now().After(request.Deadline) || msg.Subject != "nx."+a.membership.ClusterID+".reply."+a.membership.NodeID+".query."+request.ID {
			continue
		}
		reply := ownerReply{ID: request.ID, TaskID: request.TaskID, Epoch: request.Epoch}
		value, err := a.ownerRead(request)
		if err != nil {
			var problem *domain.Error
			if !errors.As(err, &problem) {
				problem = domain.Err("OWNER_UNAVAILABLE", "Owner data is unavailable.", 503)
			}
			reply.Error = problem
			reply.ErrorStatus = problem.Status
		} else {
			reply.Data, _ = json.Marshal(value)
		}
		data, err := json.Marshal(reply)
		if err != nil || len(data) > 64<<10 {
			continue
		}
		a.nc.Publish("nx."+a.membership.ClusterID+".query."+a.membership.NodeID, data)
	}
}
func (a *executorAgent) ownerRead(request ownerRequest) (any, error) {
	task, err := a.service.LocalTask(request.TaskID)
	if err != nil {
		return nil, err
	}
	if task.Scope != "cluster" || task.ClusterID == nil || *task.ClusterID != a.membership.ClusterID || task.MembershipEpoch != request.Epoch {
		return nil, domain.Err("FORBIDDEN", "This task is outside the current assignment scope.", 403)
	}
	switch request.Kind {
	case "task.logs":
		if request.Limit < 1 || request.Limit > queryChunk {
			return nil, domain.Err("INVALID_QUERY", "Invalid query chunk size.", 400)
		}
		return a.service.Logs(request.TaskID, request.Offset, request.Limit)
	case "task.log_bounds":
		log, complete, err := a.service.LogSnapshot(request.TaskID)
		if err != nil {
			return nil, err
		}
		return logBounds{Start: log.Start, End: log.End(), Complete: complete}, nil
	default:
		return nil, domain.Err("UNSUPPORTED_QUERY", "This owner query is unavailable.", 422)
	}
}
func (m *Manager) queryOwner(ctx context.Context, node string, request ownerRequest, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	member, err := m.member(node)
	if err != nil {
		return err
	}
	m.mu.RLock()
	if m.broker == nil || m.membership == nil {
		m.mu.RUnlock()
		return unavailable()
	}
	nc, cluster := m.broker.Client, m.membership.ClusterID
	m.mu.RUnlock()
	select {
	case m.querySlots <- struct{}{}:
		defer func() { <-m.querySlots }()
	default:
		return domain.Err("OWNER_UNAVAILABLE", "Owner query capacity is occupied.", 503)
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	request.ID = domain.NewID("q_")
	request.Epoch = member.Epoch
	request.Deadline, _ = ctx.Deadline()
	sub, err := nc.SubscribeSync("nx." + cluster + ".query." + node)
	if err != nil {
		return unavailable()
	}
	defer sub.Unsubscribe()
	if err = sub.SetPendingLimits(32, 2<<20); err != nil {
		return unavailable()
	}
	data, _ := json.Marshal(request)
	if err = nc.Publish("nx."+cluster+".reply."+node+".query."+request.ID, data); err != nil {
		return unavailable()
	}
	for ctx.Err() == nil {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return domain.Err("OWNER_UNAVAILABLE", "The task owner did not return current data.", 503)
		}
		var reply ownerReply
		if len(msg.Data) > 64<<10 || domain.StrictJSON(msg.Data, &reply) != nil || reply.ID != request.ID || reply.TaskID != request.TaskID || reply.Epoch != request.Epoch {
			continue
		}
		if reply.Error != nil {
			reply.Error.Status = reply.ErrorStatus
			return reply.Error
		}
		if domain.StrictJSON(reply.Data, out) != nil {
			return domain.Err("OWNER_UNAVAILABLE", "Owner response is invalid.", 503)
		}
		return nil
	}
	return ctx.Err()
}
func (m *Manager) RemoteLogs(ctx context.Context, id string, offset, limit int) (service.LogChunk, error) {
	if err := ctx.Err(); err != nil {
		return service.LogChunk{}, err
	}
	task, err := m.RemoteTask(id)
	if err != nil {
		return service.LogChunk{}, err
	}
	var chunk service.LogChunk
	err = m.queryOwner(ctx, task.OwnerNodeID, ownerRequest{Kind: "task.logs", TaskID: id, Offset: offset, Limit: min(limit, queryChunk)}, &chunk)
	return chunk, err
}
func (m *Manager) RemoteLogSnapshot(ctx context.Context, id string) (storage.Log, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	task, err := m.RemoteTask(id)
	if err != nil {
		return storage.Log{}, false, err
	}
	var bounds logBounds
	if err = m.queryOwner(ctx, task.OwnerNodeID, ownerRequest{Kind: "task.log_bounds", TaskID: id}, &bounds); err != nil {
		return storage.Log{}, false, err
	}
	if bounds.Start < 0 || bounds.End < bounds.Start || bounds.End-bounds.Start > storage.LogRetention {
		return storage.Log{}, false, domain.Err("OWNER_UNAVAILABLE", "Owner log bounds are invalid.", 503)
	}
	log := storage.Log{Start: bounds.Start, Data: make([]byte, 0, bounds.End-bounds.Start)}
	for offset := bounds.Start; offset < bounds.End; {
		var chunk service.LogChunk
		if err = m.queryOwner(ctx, task.OwnerNodeID, ownerRequest{Kind: "task.logs", TaskID: id, Offset: offset, Limit: min(queryChunk, bounds.End-offset)}, &chunk); err != nil {
			return storage.Log{}, false, err
		}
		data, err := chunk.Bytes()
		if err != nil || chunk.Offset != offset || chunk.NextOffset != offset+len(data) || len(data) == 0 || chunk.NextOffset > bounds.End {
			return storage.Log{}, false, domain.Err("OWNER_UNAVAILABLE", "Owner log range is inconsistent.", 503)
		}
		log.Data = append(log.Data, data...)
		offset = chunk.NextOffset
	}
	return log, bounds.Complete, nil
}
