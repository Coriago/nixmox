package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	js "github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/errgroup"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/task"
	"nixmox/internal/engine/telemetry"
	"nixmox/internal/logging"
)

// Run publishes the same sampler schema on every member. Disconnection drops
// realtime data; it never downgrades durable membership.
func (c *ClusterNode) Run(ctx context.Context, sampler engine.HostSampler) error {
	ticker := time.NewTicker(time.Duration(c.config.Telemetry.Host.FastIntervalMS) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		_ = c.publishPresence(ctx, sampler)
	}
}

func (c *ClusterNode) publishPresence(ctx context.Context, sampler engine.HostSampler) (err error) {
	ctx, span := telemetry.Background(ctx, "presence.publish")
	defer func() { telemetry.End(span, err) }()
	c.mu.Lock()
	transport := c.transport
	core, host := transport.Core, c.localHost()
	var version domain.Version
	if c.keys.Trust != nil && c.keys.Trust.Topology != nil {
		version = c.keys.Trust.Topology.Topology.Version
		if c.keys.Trust.BrokerTopology != nil {
			version = c.keys.Trust.BrokerTopology.Topology.Version
		}
	}
	c.mu.Unlock()
	if transport.Broker != nil {
		if info, err := transport.Broker.Diagnostics(); err == nil && info.Meta != nil {
			logging.From(ctx).Info("broker topology", "meta_leader", info.Meta.Leader, "meta_size", info.Meta.Size, "routes", transport.Broker.RouteConnections())
		}
	}
	if host.ClusterID != nil && core != nil {
		sample := sampler.Snapshot(ctx)
		sample.MembershipEpoch = host.MembershipEpoch
		err = errors.Join(core.Heartbeat(ctx, domain.Heartbeat{TopologyVersion: version, Host: host, StreamID: sample.StreamID}), core.Publish(ctx, sample))
	}
	return err
}

// Deliver keeps durable consumers on the authority. Leaves receive only their
// assigned envelope; a relay ack follows the executor's durable admission proof.
func (s *Service) Deliver(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		c := s.Cluster
		c.mu.Lock()
		transport := c.transport
		var nodes []domain.ID
		if c.state != nil && transport.Store != nil {
			for id := range c.state.Members {
				nodes = append(nodes, id)
			}
		}
		c.mu.Unlock()
		if len(nodes) == 0 {
			continue
		}
		group, life := errgroup.WithContext(transport.Context)
		for _, node := range nodes {
			group.Go(func() error {
				deliveries, err := transport.Store.Commands(life, node)
				if err != nil {
					return err
				}
				defer deliveries.Close()
				for {
					delivery, err := deliveries.Next(life)
					if err != nil {
						return err
					}
					if err = s.deliver(life, delivery); err != nil {
						_ = delivery.Retry(life)
					}
				}
			})
		}
		_ = group.Wait()
	}
}

func (s *Service) deliver(ctx context.Context, delivery engine.Delivery) (result error) {
	envelope := delivery.Envelope()
	ctx, span := telemetry.Start(telemetry.Extract(ctx, envelope.Trace), "nats.command.process", trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(attribute.String("messaging.system", "nats"), attribute.String("nixmox.command.id", string(envelope.Command.ID))))
	defer func() { telemetry.End(span, result) }()
	ctx = logging.With(ctx, "command_id", string(envelope.Command.ID))
	logging.From(ctx).Info("command received")
	c := s.Cluster
	c.mu.Lock()
	member, active := c.state.Members[envelope.OwnerNodeID]
	c.mu.Unlock()
	if !active || member.Removed || member.Epoch != envelope.MembershipEpoch {
		detail := &domain.Error{Code: "MEMBERSHIP_REQUIRED", Message: "Assignment belongs to an inactive membership epoch"}
		outcome := domain.AssignmentOutcome{Record: domain.AdmissionRecord{SchemaVersion: 1, Envelope: envelope, Receipt: domain.Receipt{CommandID: envelope.Command.ID, Admission: "rejected"}, Error: detail}}
		if err := c.recordAdmission(ctx, envelope, outcome); err != nil {
			return err
		}
		return delivery.Ack(ctx)
	}
	var outcome domain.AssignmentOutcome
	var err error
	if envelope.OwnerNodeID == s.Identity.NodeID {
		outcome, err = s.assign(ctx, envelope)
	} else {
		var data json.RawMessage
		data, err = s.Cluster.call(ctx, envelope.OwnerNodeID, "command.admit", envelope, false)
		if err == nil {
			err = json.Unmarshal(data, &outcome)
		}
	}
	if err != nil {
		var detail *domain.Error
		if !errors.As(err, &detail) || (detail.Code != "INVALID_INPUT" && detail.Code != "IDEMPOTENCY_CONFLICT" && detail.Code != "MEMBERSHIP_REQUIRED" && detail.Code != "UNSUPPORTED_CAPABILITY") {
			return err
		}
		outcome.Record = domain.AdmissionRecord{SchemaVersion: 1, Envelope: envelope, Receipt: domain.Receipt{CommandID: envelope.Command.ID, Admission: "rejected"}, Error: detail}
	}
	if err = s.Cluster.recordAdmission(ctx, envelope, outcome); err != nil {
		return err
	}
	return delivery.Ack(ctx)
}

func (s *Service) assign(ctx context.Context, envelope domain.Envelope) (domain.AssignmentOutcome, error) {
	c := s.Cluster
	c.mu.Lock()
	id := c.keys.Identity
	valid := id.ClusterID != nil && id.MembershipEpoch != nil && envelope.ClusterID != nil &&
		*id.ClusterID == *envelope.ClusterID && envelope.MembershipEpoch == *id.MembershipEpoch && envelope.OwnerNodeID == id.NodeID
	c.mu.Unlock()
	if !valid {
		return domain.AssignmentOutcome{}, &domain.Error{Code: "MEMBERSHIP_REQUIRED", Message: "Assignment does not match current membership"}
	}
	hash, err := task.RequestHash(envelope.Command)
	if err != nil || hash != envelope.RequestHash {
		return domain.AssignmentOutcome{}, invalid("Invalid assignment fingerprint")
	}
	if _, _, err = s.Registry.Decode(envelope.Command); err != nil {
		return domain.AssignmentOutcome{}, err
	}
	var admitErr error
	control := envelope.Command.Type == "task.cancel" || envelope.Command.Type == "task.retry"
	if control {
		_, admitErr = s.Scheduler.Control(ctx, envelope)
	} else {
		_, admitErr = s.Scheduler.AdmitAssigned(ctx, envelope)
	}
	record, err := s.Journal.Receipt(ctx, envelope.Command.ID)
	if err != nil {
		return domain.AssignmentOutcome{}, admitErr
	}
	if record.Envelope.RequestHash != hash {
		return domain.AssignmentOutcome{}, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Executor command ID already used"}
	}
	outcome := domain.AssignmentOutcome{Record: record}
	if record.Receipt.TaskID != nil && envelope.Command.Type != "task.cancel" {
		current, err := s.Journal.Task(ctx, *record.Receipt.TaskID)
		if err != nil {
			return outcome, err
		}
		outcome.Task = &current
	}
	return outcome, nil
}

func (c *ClusterNode) recordAdmission(ctx context.Context, envelope domain.Envelope, outcome domain.AssignmentOutcome) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.quorum(ctx); err != nil {
		return err
	}
	r := outcome.Record
	if r.Envelope.RequestHash != envelope.RequestHash || r.Envelope.OwnerNodeID != envelope.OwnerNodeID || r.Receipt.CommandID != envelope.Command.ID {
		return invalid("Executor admission proof mismatch")
	}
	key := recordKey("receipt", envelope.Command.ID)
	prior, revision, err := readRecord[domain.AdmissionRecord](ctx, c.transport.Store, key)
	if err != nil && !errors.Is(err, js.ErrKeyNotFound) {
		return err
	}
	if err == nil && prior.Envelope.RequestHash != r.Envelope.RequestHash {
		return invalid("Stored admission fingerprint mismatch")
	}
	if _, err = writeRecord(ctx, c.transport.Store, key, revision, r); err != nil {
		return err
	}
	if outcome.Task != nil {
		return c.projectTask(ctx, envelope, *outcome.Task)
	}
	return nil
}

func (c *ClusterNode) Receipt(ctx context.Context, id domain.ID) (domain.AdmissionRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport.Store == nil {
		return domain.AdmissionRecord{}, os.ErrNotExist
	}
	record, _, err := readRecord[domain.AdmissionRecord](ctx, c.transport.Store, recordKey("receipt", id))
	if errors.Is(err, js.ErrKeyNotFound) {
		err = os.ErrNotExist
	}
	return record, err
}
