package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
	"nixmox/internal/service"
)

type executionResult struct {
	Kind        string          `json:"kind"`
	NodeID      string          `json:"node_id"`
	Epoch       int             `json:"membership_epoch"`
	CommandID   string          `json:"command_id"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	Receipt     *domain.Receipt `json:"receipt,omitempty"`
	Error       *domain.Error   `json:"error,omitempty"`
	ErrorStatus int             `json:"error_status,omitempty"`
	Task        *domain.Task    `json:"task,omitempty"`
	ObservedAt  time.Time       `json:"observed_at"`
}

type executorAgent struct {
	nc         *nats.Conn
	service    *service.Service
	membership Membership
	ctx        context.Context
	cancel     context.CancelFunc
	workers    sync.WaitGroup
	sub        *nats.Subscription
	querySub   *nats.Subscription
}

func newExecutorAgent(nc *nats.Conn, s *service.Service, m Membership) (*executorAgent, error) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &executorAgent{nc: nc, service: s, membership: m, ctx: ctx, cancel: cancel}
	if !m.Provisional {
		// Raw command publications are never executed. Only a full service's
		// forwarding of a committed durable-consumer delivery reaches this subject.
		sub, err := nc.SubscribeSync("nx." + m.ClusterID + ".reply." + m.NodeID + ".delivery")
		if err != nil {
			cancel()
			return nil, err
		}
		if err = sub.SetPendingLimits(domain.QueueCapacity, 8<<20); err != nil {
			sub.Unsubscribe()
			cancel()
			return nil, err
		}
		a.sub = sub
	}
	query, err := nc.SubscribeSync("nx." + m.ClusterID + ".reply." + m.NodeID + ".query.*")
	if err != nil {
		if a.sub != nil {
			a.sub.Unsubscribe()
		}
		cancel()
		return nil, err
	}
	if err = query.SetPendingLimits(32, 128<<10); err != nil {
		query.Unsubscribe()
		if a.sub != nil {
			a.sub.Unsubscribe()
		}
		cancel()
		return nil, err
	}
	a.querySub = query
	if a.sub != nil {
		a.workers.Add(1)
		go a.deliveries()
	}
	a.workers.Add(2)
	go a.snapshots()
	go a.queries()
	return a, nil
}
func (a *executorAgent) Close() {
	a.cancel()
	if a.sub != nil {
		a.sub.Unsubscribe()
	}
	a.querySub.Unsubscribe()
	a.workers.Wait()
}
func (a *executorAgent) deliveries() {
	defer a.workers.Done()
	for a.ctx.Err() == nil {
		msg, err := a.sub.NextMsg(250 * time.Millisecond)
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
		var p domain.PreparedCommand
		if len(msg.Data) > domain.MaxCommandBytes || domain.StrictJSON(msg.Data, &p) != nil || p.ClusterID != a.membership.ClusterID || p.Command.TargetID != a.membership.NodeID || p.TargetEpoch != a.membership.Epoch {
			continue
		}
		receipt, err := a.service.AdmitDelivered(p)
		result := executionResult{Kind: "receipt", NodeID: a.membership.NodeID, Epoch: a.membership.Epoch, CommandID: p.Command.ID, Fingerprint: p.Fingerprint, ObservedAt: time.Now().UTC()}
		if err == nil {
			result.Receipt = &receipt
			task, taskErr := a.service.Task(receipt.TaskID)
			if taskErr != nil {
				continue
			}
			task = projectableTask(task)
			result.Task = &task
		} else {
			var problem *domain.Error
			if !errors.As(err, &problem) || problem.Code == "OUTCOME_UNKNOWN" || problem.Code == "UNAVAILABLE" {
				continue
			}
			result.Error = problem
			result.ErrorStatus = problem.Status
		}
		// Lost acknowledgments are reconciled by delivery of the same command.
		// The local receipt is already durable before any result leaves this node.
		_ = a.publishReceipt(result)
	}
}
func projectableTask(task domain.Task) domain.Task {
	task.Checkpoint = nil
	task.Exclusions = nil
	task.WaitingFor = nil
	return task
}
func (a *executorAgent) publishReceipt(result executionResult) error {
	identity := result
	identity.ObservedAt = time.Time{}
	identity.Task = nil
	encoded, _ := json.Marshal(identity)
	digest := sha256.Sum256(encoded)
	return a.publish(result, "receipt_"+hex.EncodeToString(digest[:]))
}
func (a *executorAgent) publish(result executionResult, id string) error {
	data, err := json.Marshal(result)
	if err != nil || len(data) > domain.MaxCommandBytes {
		return domain.Err("RESULT_UNAVAILABLE", "Execution projection exceeds its transport limit.", 503)
	}
	reply := "nx." + a.membership.ClusterID + ".reply." + a.membership.NodeID + "." + domain.NewID("persist_")
	sub, err := a.nc.SubscribeSync(reply)
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	if err = sub.SetPendingLimits(1, 4096); err != nil {
		return err
	}
	msg := nats.NewMsg("nx." + a.membership.ClusterID + ".result." + a.membership.NodeID)
	msg.Reply = reply
	msg.Data = data
	msg.Header.Set(nats.MsgIdHdr, id)
	if err = a.nc.PublishMsg(msg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	ack, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		return err
	}
	var response struct {
		nats.PubAck
		Error *nats.APIError `json:"error,omitempty"`
	}
	if len(ack.Data) > 4096 || json.Unmarshal(ack.Data, &response) != nil || response.Error != nil || response.Stream != "nx_results" || response.Sequence == 0 {
		return unavailable()
	}
	return nil
}
func (a *executorAgent) snapshots() {
	defer a.workers.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	type sent struct {
		version int64
		offset  int
		at      time.Time
	}
	last := map[string]sent{}
	receipts := map[string]bool{}
	for a.ctx.Err() == nil {
		count := 0
		for _, local := range a.service.ExecutionReceipts() {
			if a.ctx.Err() != nil || count >= 16 {
				break
			}
			if local.ClusterID != a.membership.ClusterID || local.Epoch != a.membership.Epoch || receipts[local.Receipt.CommandID] {
				continue
			}
			count++
			task, err := a.service.Task(local.Receipt.TaskID)
			if err != nil {
				continue
			}
			task = projectableTask(task)
			result := executionResult{Kind: "receipt", NodeID: a.membership.NodeID, Epoch: a.membership.Epoch, CommandID: local.Receipt.CommandID, Fingerprint: local.Fingerprint, Receipt: &local.Receipt, Task: &task, ObservedAt: time.Now().UTC()}
			if a.publishReceipt(result) == nil {
				receipts[local.Receipt.CommandID] = true
			}
		}
		count = 0
		for _, task := range a.service.Tasks() {
			if a.ctx.Err() != nil || count >= 16 {
				break
			}
			if task.Scope != "cluster" || task.ClusterID == nil || *task.ClusterID != a.membership.ClusterID || task.MembershipEpoch != a.membership.Epoch {
				continue
			}
			old, ok := last[task.ID]
			if ok && old.version == task.Version && old.offset == task.Log.NextOffset && (task.Terminal() || time.Since(old.at) < 10*time.Second) {
				continue
			}
			count++
			// Execution checkpoints/resource locks remain authoritative on the owner.
			task = projectableTask(task)
			now := time.Now().UTC()
			result := executionResult{Kind: "task", NodeID: a.membership.NodeID, Epoch: a.membership.Epoch, CommandID: task.CommandID, Task: &task, ObservedAt: now}
			id := fmt.Sprintf("%s_%d_%d_%d", task.ID, task.Version, task.Log.NextOffset, now.Unix()/10)
			if a.publish(result, id) == nil {
				last[task.ID] = sent{task.Version, task.Log.NextOffset, now}
			}
		}
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
