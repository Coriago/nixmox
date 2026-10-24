package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	js "github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/comms"
	"nixmox/internal/engine/platform"
	"nixmox/internal/engine/task"
	"nixmox/internal/engine/telemetry"
	"nixmox/internal/logging"
)

// Run publishes the same sampler schema on every member. Disconnection drops
// realtime data; it never downgrades durable membership.
func (c *ClusterNode) Run(ctx context.Context, sampler engine.HostSampler) error {
	samples, err := sampler.Subscribe(ctx)
	if err != nil {
		return err
	}
	defer samples.Close()
	for {
		sample, err := samples.Next(ctx)
		if err != nil {
			return err
		}
		_ = c.publishPresence(ctx, sample)
	}
}

func (c *ClusterNode) publishPresence(ctx context.Context, sample domain.HostTelemetry) (err error) {
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
	if c.cache != nil {
		host.Cache = c.cache.Advertisement()
	}
	if host.ClusterID != nil && core != nil {
		sample.MembershipEpoch = host.MembershipEpoch
		boot, _ := platform.BootID()
		err = errors.Join(core.Heartbeat(ctx, domain.Heartbeat{BootID: boot, TopologyVersion: version, Host: host, StreamID: sample.StreamID}), core.Publish(ctx, sample))
	}
	return err
}

// Each host pulls its own pre-created consumer; there is no authority relay,
// persisted admission receipt, or task projection writer.
func (s *Service) consumeTasks(ctx context.Context, api js.JetStream, cluster domain.ID, control bool) error {
	consumer, err := api.Consumer(ctx, comms.TaskStream, comms.TaskConsumer(s.Identity.NodeID, control))
	if err != nil {
		return err
	}
	sub, err := consumer.Consume(func(msg js.Msg) {
		var envelope domain.Envelope
		if json.Unmarshal(msg.Data(), &envelope) != nil || envelope.OwnerNodeID != s.Identity.NodeID || (envelope.Scope == domain.ClusterScope && (envelope.ClusterID == nil || *envelope.ClusterID != cluster)) {
			_ = msg.Term()
			return
		}
		isControl := envelope.Command.Type == "task.cancel" || envelope.Command.Type == "task.retry"
		if control != isControl {
			_ = msg.Term()
			return
		}
		op, _, err := s.Registry.Decode(envelope.Command)
		if err != nil || (op.Scope != domain.Inherited && op.Scope != envelope.Scope) {
			_ = msg.Term()
			return
		}
		var definition engine.TaskDefinition
		if op.define != nil {
			definition = op.define(s)
		}
		receipt, err := s.Tasks.Accept(ctx, envelope, definition, func(ctx context.Context) error {
			ack, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return msg.DoubleAck(ack)
		})
		if errors.Is(err, task.ErrResourceBusy) {
			_ = msg.NakWithDelay(time.Second)
		} else if err != nil {
			var detail *domain.Error
			if errors.As(err, &detail) {
				// Rejected work is terminal; shutdown before ack leaves queued work intact.
				_ = msg.Term()
				if _, exists := s.Tasks.Task(ctx, task.TaskID(envelope.Command.ID)); exists != nil {
					now := time.Now().UTC()
					s.publishTask(ctx, domain.Task{Record: domain.Record{SchemaVersion: 1, ID: task.TaskID(envelope.Command.ID), Version: 1}, CommandID: envelope.Command.ID, Kind: envelope.Command.Type, Scope: envelope.Scope, ClusterID: envelope.ClusterID, MembershipEpoch: envelope.MembershipEpoch, OwnerNodeID: s.Identity.NodeID, TargetID: envelope.Command.TargetID, State: domain.Failed, CreatedAt: now, UpdatedAt: now, Error: detail})
				}
			} else {
				_ = msg.NakWithDelay(time.Second) // Ack could not be confirmed; no work started.
			}
			logging.From(ctx).Warn("task dispatch failed", "command_id", envelope.Command.ID, "error", err)
		} else {
			logging.From(ctx).Info("task started", "command_id", receipt.CommandID)
		}
	}, js.PullMaxMessages(8))
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		sub.Stop()
		<-sub.Closed()
		return ctx.Err()
	case <-sub.Closed():
		return errors.New("task consumer stopped")
	}
}
