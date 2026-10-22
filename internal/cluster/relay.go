package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
)

type deliveryHub struct {
	d       *dispatcher
	nc      *nats.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
	results *nats.Subscription
	remotes map[string]context.CancelFunc
}
type taskProjection struct {
	Task       domain.Task `json:"task"`
	ObservedAt time.Time   `json:"observed_at"`
}

func newDeliveryHub(d *dispatcher, nc *nats.Conn) (*deliveryHub, error) {
	ctx, cancel := context.WithCancel(d.ctx)
	h := &deliveryHub{d: d, nc: nc, ctx: ctx, cancel: cancel, remotes: map[string]context.CancelFunc{}}
	// Result processing only reconciles versioned projections and receipts; a
	// short redelivery interval recovers an interrupted consumer without rerunning
	// target effects. Concurrent redeliveries are fenced by KV revisions.
	_, err := d.stores.JS.AddConsumer(d.stores.Results, &nats.ConsumerConfig{Durable: "execution_results", FilterSubject: "nx." + d.cluster + ".result.*", AckPolicy: nats.AckExplicitPolicy, AckWait: 5 * time.Second, MaxDeliver: 32, MaxAckPending: 64, MaxWaiting: 8, MaxRequestBatch: 16, MaxRequestExpires: 5 * time.Second})
	if err != nil {
		cancel()
		return nil, err
	}
	h.results, err = d.stores.JS.PullSubscribe("nx."+d.cluster+".result.*", "execution_results", nats.Bind(d.stores.Results, "execution_results"), nats.ManualAck())
	if err != nil {
		cancel()
		return nil, err
	}
	h.workers.Add(2)
	go h.receive()
	go h.members()
	return h, nil
}
func (h *deliveryHub) Close() { h.cancel(); h.workers.Wait(); h.results.Unsubscribe() }
func (h *deliveryHub) receive() {
	defer h.workers.Done()
	for h.ctx.Err() == nil {
		messages, err := h.results.Fetch(1, nats.MaxWait(250*time.Millisecond))
		if err != nil {
			select {
			case <-h.ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		for _, msg := range messages {
			err = h.receiveResult(msg)
			if err == nil {
				h.d.ack(msg)
			} else {
				msg.NakWithDelay(time.Second)
			}
		}
	}
}
func (h *deliveryHub) receiveResult(msg *nats.Msg) error {
	var result executionResult
	if domain.StrictJSON(msg.Data, &result) != nil || msg.Subject != "nx."+h.d.cluster+".result."+result.NodeID || !domain.SafeID.MatchString(result.CommandID) {
		return nil
	}
	member, err := h.d.target(result.NodeID)
	if err != nil {
		return err
	}
	if member.Epoch != result.Epoch {
		return nil
	}
	record, revision, err := h.d.read(result.CommandID)
	if err != nil {
		return err
	}
	p := record.Prepared
	if p.ExecutionNode() != result.NodeID || p.ExecutionEpoch() != result.Epoch {
		return nil
	}
	switch result.Kind {
	case "receipt":
		if result.Fingerprint != p.Fingerprint || record.State == "accepted" || record.State == "rejected" {
			return nil
		}
		if record.State != "queued" {
			return unavailable()
		}
		if result.Receipt != nil && result.Error == nil {
			expected := domain.CommandTaskID(p.Command.ID)
			if p.Command.Type == "task.cancel" {
				var payload struct {
					TaskID string `json:"task_id"`
				}
				if domain.StrictJSON(p.Command.Payload, &payload) != nil {
					return nil
				}
				expected = payload.TaskID
			}
			if result.Receipt.CommandID != p.Command.ID || result.Receipt.TaskID != expected || result.Receipt.Admission != "accepted" {
				return nil
			}
			if result.Task == nil || result.Task.ID != expected {
				return nil
			}
			if err = h.d.projectTask(*result.Task, result.ObservedAt); err != nil {
				return err
			}
			record.State = "accepted"
			record.Receipt = result.Receipt
		} else if result.Error != nil && result.Receipt == nil && result.ErrorStatus >= 400 && result.ErrorStatus < 600 {
			record.State = "rejected"
			record.Error = result.Error
			record.ErrorStatus = result.ErrorStatus
		} else {
			return nil
		}
		return h.d.update(record, revision)
	case "task":
		task := result.Task
		if task == nil || task.ID != domain.CommandTaskID(result.CommandID) || task.OwnerNodeID != result.NodeID || task.TargetID != p.Command.TargetID || task.Scope != "cluster" || task.ClusterID == nil || *task.ClusterID != h.d.cluster || task.MembershipEpoch != member.Epoch || task.Version < 1 || task.CoordinatorID != p.CoordinatorID || task.Kind != p.Kind {
			return nil
		}
		if result.ObservedAt.After(time.Now().Add(5 * time.Second)) {
			return nil
		}
		return h.d.projectTask(*task, result.ObservedAt)
	default:
		return nil
	}
}

func (d *dispatcher) projectTask(task domain.Task, observed time.Time) error {
	record, _, err := d.read(task.CommandID)
	if err != nil {
		return err
	}
	p := record.Prepared
	if task.ID != domain.CommandTaskID(task.CommandID) || task.TargetID != p.Command.TargetID || task.OwnerNodeID != p.ExecutionNode() || task.ClusterID == nil || *task.ClusterID != d.cluster || task.Scope != "cluster" || task.MembershipEpoch != p.ExecutionEpoch() || task.CoordinatorID != p.CoordinatorID || task.Kind != p.Kind || observed.IsZero() || observed.After(time.Now().Add(5*time.Second)) {
		return domain.Err("INVALID_RESULT", "Task result has an invalid execution binding.", 422)
	}
	task.Checkpoint = nil
	task.Exclusions = nil
	task.WaitingFor = nil
	key := "task." + task.ID
	data, err := json.Marshal(taskProjection{Task: task, ObservedAt: observed})
	if err != nil || len(data) > domain.MaxCommandBytes {
		return domain.Err("RESULT_UNAVAILABLE", "Task projection exceeds its storage limit.", 503)
	}
	old, err := d.stores.State.Get(key)
	if errors.Is(err, nats.ErrKeyNotFound) {
		_, err = d.stores.State.Create(key, data)
	} else if err == nil {
		var existing taskProjection
		if domain.StrictJSON(old.Value(), &existing) != nil {
			return unavailable()
		}
		if existing.Task.Version > task.Version || existing.Task.Version == task.Version && existing.ObservedAt.After(observed) {
			return nil
		}
		if existing.Task.Terminal() {
			before, _ := json.Marshal(existing.Task)
			after, _ := json.Marshal(task)
			if string(before) != string(after) {
				return domain.Err("INVALID_RESULT", "Terminal task projections are immutable.", 409)
			}
			return nil
		}
		_, err = d.stores.State.Update(key, data, old.Revision())
	}
	if err == nil {
		d.service.NotifyChanges()
	}
	return err
}
func (h *deliveryHub) members() {
	defer h.workers.Done()
	defer func() {
		for _, cancel := range h.remotes {
			cancel()
		}
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for h.ctx.Err() == nil {
		keys, err := h.d.stores.State.Keys()
		if err == nil && len(keys) <= domain.MaxRecords+domain.QueueCapacity {
			seen := map[string]bool{}
			complete := true
			deadline := time.Now().Add(3 * time.Second)
			for _, key := range keys {
				if !strings.HasPrefix(key, "member.") {
					continue
				}
				if h.ctx.Err() != nil || time.Now().After(deadline) {
					complete = false
					break
				}
				id := strings.TrimPrefix(key, "member.")
				member, err := h.d.target(id)
				if err != nil {
					complete = false
					continue
				}
				if member.BrokerRole != "leaf" {
					continue
				}
				seen[id] = true
				if h.remotes[id] != nil || len(h.remotes) >= 255 {
					continue
				}
				ctx, cancel := context.WithCancel(h.ctx)
				sub, err := h.consumer(id)
				if err != nil {
					cancel()
					continue
				}
				h.remotes[id] = cancel
				h.workers.Add(1)
				go h.forward(ctx, id, sub)
			}
			if complete {
				for id, cancel := range h.remotes {
					if !seen[id] {
						cancel()
						delete(h.remotes, id)
					}
				}
			}
		}
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (h *deliveryHub) consumer(node string) (*nats.Subscription, error) {
	durable := "executor_" + node
	subject := "nx." + h.d.cluster + ".command." + node
	_, err := h.d.stores.JS.AddConsumer(h.d.stores.Commands, &nats.ConsumerConfig{Durable: durable, FilterSubject: subject, AckPolicy: nats.AckExplicitPolicy, AckWait: 10 * time.Second, MaxDeliver: 64, MaxAckPending: 32, MaxWaiting: 8, MaxRequestBatch: 1, MaxRequestExpires: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	return h.d.stores.JS.PullSubscribe(subject, durable, nats.Bind(h.d.stores.Commands, durable), nats.ManualAck())
}
func (h *deliveryHub) forward(ctx context.Context, node string, sub *nats.Subscription) {
	defer h.workers.Done()
	defer sub.Unsubscribe()
	for ctx.Err() == nil {
		messages, err := sub.Fetch(1, nats.MaxWait(250*time.Millisecond))
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		for _, msg := range messages {
			var wire domain.PreparedCommand
			if domain.StrictJSON(msg.Data, &wire) != nil || wire.ClusterID != h.d.cluster || wire.ExecutionNode() != node {
				msg.Term()
				continue
			}
			record, _, err := h.d.read(wire.Command.ID)
			if err != nil {
				msg.NakWithDelay(time.Second)
				continue
			}
			if record.Prepared.Fingerprint != wire.Fingerprint {
				msg.Term()
				continue
			}
			if record.State == "accepted" || record.State == "rejected" {
				h.d.ack(msg)
				continue
			}
			member, err := h.d.target(node)
			if err != nil || member.Epoch != record.Prepared.ExecutionEpoch() || record.State != "queued" {
				msg.NakWithDelay(2 * time.Second)
				continue
			}
			data, _ := json.Marshal(record.Prepared)
			_ = h.nc.Publish("nx."+h.d.cluster+".reply."+node+".delivery", data)
			// Only a quorum-committed execution receipt authorizes the command ack.
			// Durable result consumption runs independently of this forwarding loop.
			msg.NakWithDelay(2 * time.Second)
		}
	}
}
