package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/broker"
	"nixmox/internal/domain"
	"nixmox/internal/service"
)

type commandRecord struct {
	SchemaVersion int                    `json:"schema_version"`
	OwnerNodeID   string                 `json:"owner_node_id"`
	State         string                 `json:"state"`
	Prepared      domain.PreparedCommand `json:"prepared"`
	Receipt       *domain.Receipt        `json:"receipt,omitempty"`
	Error         *domain.Error          `json:"error,omitempty"`
	ErrorStatus   int                    `json:"error_status,omitempty"`
}
type dispatcher struct {
	stores        *broker.Stores
	service       *service.Service
	cluster, node string
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	serial        chan struct{}
	repairOffset  int
	capacity      chan struct{}
	sub           *nats.Subscription
	target        func(string) (Member, error)
}

func newDispatcher(stores *broker.Stores, s *service.Service, cluster, node string, target func(string) (Member, error)) (*dispatcher, error) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &dispatcher{stores: stores, service: s, cluster: cluster, node: node, ctx: ctx, cancel: cancel, done: make(chan struct{}), capacity: make(chan struct{}, domain.QueueCapacity), serial: make(chan struct{}, 1)}
	d.target = target
	durable := "executor_" + node
	subject := "nx." + cluster + ".command." + node
	_, err := stores.JS.AddConsumer(stores.Commands, &nats.ConsumerConfig{Durable: durable, FilterSubject: subject, AckPolicy: nats.AckExplicitPolicy, AckWait: 30 * time.Second, MaxDeliver: 10, MaxAckPending: 32, MaxWaiting: 32, MaxRequestBatch: 32, MaxRequestExpires: 5 * time.Second})
	if err != nil {
		cancel()
		return nil, err
	}
	sub, err := stores.JS.PullSubscribe(subject, durable, nats.Bind(stores.Commands, durable), nats.ManualAck())
	if err != nil {
		cancel()
		return nil, err
	}
	d.sub = sub
	go d.work()
	return d, nil
}
func (d *dispatcher) Close() { d.cancel(); <-d.done; d.sub.Unsubscribe() }
func (d *dispatcher) read(id string) (commandRecord, uint64, error) {
	entry, err := d.stores.State.Get("command." + id)
	if err != nil {
		return commandRecord{}, 0, err
	}
	var record commandRecord
	if domain.StrictJSON(entry.Value(), &record) != nil || record.SchemaVersion != 1 || record.Prepared.Command.ID != id || record.Prepared.ClusterID != d.cluster {
		return record, 0, domain.Err("UNAVAILABLE", "Durable command record is invalid.", 503)
	}
	return record, entry.Revision(), nil
}
func (d *dispatcher) update(record commandRecord, revision uint64) error {
	b, err := json.Marshal(record)
	if err != nil || len(b) > domain.MaxCommandBytes {
		return domain.Err("INVALID_PAYLOAD", "Prepared command exceeds the durable record limit.", 422)
	}
	_, err = d.stores.State.Update("command."+record.Prepared.Command.ID, b, revision)
	return err
}
func (record commandRecord) outcome() (domain.Receipt, error) {
	if record.State == "accepted" && record.Receipt != nil {
		return *record.Receipt, nil
	}
	if record.State == "rejected" && record.Error != nil {
		copy := *record.Error
		copy.Status = record.ErrorStatus
		return domain.Receipt{}, &copy
	}
	return domain.Receipt{}, domain.Err("OUTCOME_UNKNOWN", "Command is durably recorded; query this command ID before retrying.", 503)
}
func (d *dispatcher) Receipt(id string) (domain.Receipt, error) {
	if !domain.SafeID.MatchString(id) {
		return domain.Receipt{}, domain.Err("INVALID_QUERY", "Invalid command ID.", 400)
	}
	record, _, err := d.read(id)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return domain.Receipt{}, domain.Err("NOT_FOUND", "Command not found.", 404)
	}
	if err != nil {
		return domain.Receipt{}, unavailable()
	}
	return record.outcome()
}
func (d *dispatcher) Submit(c domain.Command) (domain.Receipt, error) {
	c, fingerprint, err := domain.CanonicalCommand(c)
	if err != nil {
		return domain.Receipt{}, err
	}
	select {
	case d.capacity <- struct{}{}:
		defer func() { <-d.capacity }()
	default:
		return domain.Receipt{}, domain.Err("QUEUE_FULL", "Cluster ingress capacity is exhausted.", 503)
	}
	// Serializing this coordinator's preparations prevents duplicate concurrent
	// requests from resolving a mutable selector twice. KV CAS fences other nodes.
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case d.serial <- struct{}{}:
		defer func() { <-d.serial }()
	case <-d.ctx.Done():
		return domain.Receipt{}, unavailable()
	case <-timer.C:
		return domain.Receipt{}, domain.Err("QUEUE_FULL", "Cluster preparation capacity is occupied.", 503)
	}
	if d.ctx.Err() != nil {
		return domain.Receipt{}, unavailable()
	}
	record, revision, err := d.read(c.ID)
	if errors.Is(err, nats.ErrKeyNotFound) {
		if err := d.service.ValidateCluster(c); err != nil {
			return domain.Receipt{}, err
		}
		member, targetErr := d.target(c.TargetID)
		if targetErr != nil {
			return domain.Receipt{}, targetErr
		}
		status, statusErr := d.stores.State.Status()
		if statusErr != nil {
			return domain.Receipt{}, unavailable()
		}
		if status.Values() >= domain.MaxRecords {
			return domain.Receipt{}, domain.Err("QUEUE_FULL", "Durable command capacity is exhausted.", 503)
		}
		now := time.Now().UTC()
		record = commandRecord{SchemaVersion: 1, OwnerNodeID: d.node, State: "resolving", Prepared: domain.PreparedCommand{Command: c, ClusterID: d.cluster, Fingerprint: fingerprint, Kind: c.Type, AdmittedAt: now, ExpiresAt: now.Add(120 * time.Second)}}
		record.Prepared.TargetEpoch = member.Epoch
		record.Prepared.CoordinatorID = d.node
		b, _ := json.Marshal(record)
		if len(b) > domain.MaxCommandBytes {
			return domain.Receipt{}, domain.Err("INVALID_PAYLOAD", "Command exceeds the durable record limit.", 422)
		}
		revision, err = d.stores.State.Create("command."+c.ID, b)
		if err != nil {
			record, revision, err = d.read(c.ID)
		}
	}
	if err != nil {
		return domain.Receipt{}, unavailable()
	}
	if record.Prepared.Fingerprint != fingerprint {
		return domain.Receipt{}, domain.Err("IDEMPOTENCY_CONFLICT", "Command ID is already bound to another request.", 409)
	}
	if record.State == "rejected" && record.Error != nil && record.Error.Retryable {
		if time.Now().After(record.Prepared.ExpiresAt) {
			return domain.Receipt{}, domain.Err("COMMAND_EXPIRED", "Command expired before admission.", 409)
		}
		record.State = "queued"
		record.Error = nil
		record.ErrorStatus = 0
		if err = d.update(record, revision); err != nil {
			return domain.Receipt{}, unavailable()
		}
	}
	if record.State == "accepted" || record.State == "rejected" {
		return record.outcome()
	}
	if record.State == "resolving" {
		if record.OwnerNodeID != d.node {
			return record.outcome()
		}
		if time.Now().After(record.Prepared.ExpiresAt) {
			return domain.Receipt{}, domain.Err("COMMAND_EXPIRED", "Command expired before admission.", 409)
		}
		prepared, err := d.service.PrepareCluster(d.ctx, c, d.cluster)
		if err != nil {
			var rejected *domain.Error
			if errors.As(err, &rejected) && rejected.Status >= 400 && rejected.Status < 500 {
				record.State = "rejected"
				record.Error = rejected
				record.ErrorStatus = rejected.Status
				if writeErr := d.update(record, revision); writeErr != nil {
					return domain.Receipt{}, domain.Err("OUTCOME_UNKNOWN", "Admission decision durability is uncertain.", 503)
				}
			}
			return domain.Receipt{}, err
		}
		prepared.AdmittedAt = record.Prepared.AdmittedAt
		prepared.TargetEpoch = record.Prepared.TargetEpoch
		prepared.CoordinatorID = record.OwnerNodeID
		prepared.ExpiresAt = record.Prepared.ExpiresAt
		record.Prepared = prepared
		record.State = "queued"
		if err = d.update(record, revision); err != nil {
			return domain.Receipt{}, domain.Err("OUTCOME_UNKNOWN", "Pinned admission durability is uncertain; query this command ID.", 503)
		}
	}
	if err = d.publish(record); err != nil {
		return domain.Receipt{}, domain.Err("OUTCOME_UNKNOWN", "Command delivery is uncertain; query this command ID.", 503)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		current, _, err := d.read(c.ID)
		if err == nil && (current.State == "accepted" || current.State == "rejected") {
			return current.outcome()
		}
		select {
		case <-d.ctx.Done():
			return domain.Receipt{}, unavailable()
		case <-deadline.C:
			return record.outcome()
		case <-tick.C:
		}
	}
}
func (d *dispatcher) publish(record commandRecord) error {
	b, err := json.Marshal(record.Prepared)
	if err != nil {
		return err
	}
	msg := nats.NewMsg("nx." + d.cluster + ".command." + record.Prepared.Command.TargetID)
	msg.Data = b
	msg.Header.Set(nats.MsgIdHdr, record.Prepared.Command.ID)
	ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	defer cancel()
	_, err = d.stores.JS.PublishMsg(msg, nats.Context(ctx))
	return err
}
func (d *dispatcher) work() {
	defer close(d.done)
	repair := time.NewTicker(5 * time.Second)
	defer repair.Stop()
	for d.ctx.Err() == nil {
		messages, err := d.sub.Fetch(1, nats.MaxWait(250*time.Millisecond))
		if err == nil {
			for _, msg := range messages {
				d.consume(msg)
			}
		}
		if err != nil && !errors.Is(err, nats.ErrTimeout) {
			select {
			case <-d.ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		select {
		case <-repair.C:
			d.repair()
		default:
		}
	}
}
func (d *dispatcher) consume(msg *nats.Msg) {
	var wire domain.PreparedCommand
	if domain.StrictJSON(msg.Data, &wire) != nil || wire.ClusterID != d.cluster || wire.Command.TargetID != d.node {
		msg.Term()
		return
	}
	record, revision, err := d.read(wire.Command.ID)
	if err != nil {
		msg.NakWithDelay(time.Second)
		return
	}
	if wire.Fingerprint != record.Prepared.Fingerprint {
		msg.Term()
		return
	}
	if record.State == "accepted" || record.State == "rejected" {
		d.ack(msg)
		return
	}
	if record.State != "queued" {
		msg.NakWithDelay(time.Second)
		return
	}
	if d.admitRecord(record, revision) == nil {
		d.ack(msg)
	} else {
		msg.NakWithDelay(time.Second)
	}
}
func (d *dispatcher) ack(msg *nats.Msg) {
	ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	defer cancel()
	msg.AckSync(nats.Context(ctx))
}
func (d *dispatcher) admitRecord(record commandRecord, revision uint64) error {
	receipt, err := d.service.AdmitDelivered(record.Prepared)
	if err == nil {
		task, taskErr := d.service.Task(receipt.TaskID)
		if taskErr != nil {
			return taskErr
		}
		if err = d.projectTask(task, time.Now().UTC()); err != nil {
			return err
		}
		record.State = "accepted"
		record.Receipt = &receipt
	} else {
		var e *domain.Error
		if !errors.As(err, &e) || e.Code == "OUTCOME_UNKNOWN" || e.Code == "UNAVAILABLE" {
			return err
		}
		record.State = "rejected"
		record.Error = e
		record.ErrorStatus = e.Status
	}
	return d.update(record, revision)
}
func (d *dispatcher) repair() {
	keys, err := d.stores.State.Keys()
	if err != nil || len(keys) > domain.MaxRecords+domain.QueueCapacity {
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	count := 0
	for count < min(32, len(keys)) {
		d.repairOffset %= len(keys)
		key := keys[d.repairOffset]
		d.repairOffset++
		count++
		if d.ctx.Err() != nil || time.Now().After(deadline) || count >= 32 {
			return
		}
		if !strings.HasPrefix(key, "command.") {
			continue
		}
		record, revision, err := d.read(strings.TrimPrefix(key, "command."))
		if err != nil || record.State != "queued" || record.Prepared.Command.TargetID != d.node {
			continue
		}
		if time.Now().After(record.Prepared.ExpiresAt) {
			d.admitRecord(record, revision)
		} else {
			d.publish(record)
		}
	}
}
