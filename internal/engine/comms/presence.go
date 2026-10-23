package comms

import (
	"context"
	"encoding/json"

	"github.com/nats-io/nats.go"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

func (c *Core) Heartbeat(ctx context.Context, heartbeat domain.Heartbeat) error {
	if heartbeat.Host.ID != c.Node {
		return &domain.Error{Code: "INVALID_INPUT", Message: "heartbeat owner mismatch"}
	}
	return c.publish(ctx, Subject(c.Cluster, "heartbeat", c.Node), heartbeat, domain.CommandPayloadLimit)
}

// ObservePresence is for full/provisional service clients only. Accepting leaf
// brokers enforce sender subjects; this adapter also binds each decoded owner to
// its exact subject. Membership epochs and boot/stream continuity are policy.
func (c *Core) ObservePresence(heartbeat func(domain.Heartbeat), sample func(domain.HostTelemetry)) error {
	prefix := "nx." + shared.Token(c.Cluster) + "."
	handlers := map[string]nats.MsgHandler{
		"heartbeat": func(m *nats.Msg) {
			var value domain.Heartbeat
			if len(m.Data) <= domain.CommandPayloadLimit && json.Unmarshal(m.Data, &value) == nil && m.Subject == Subject(c.Cluster, "heartbeat", value.Host.ID) {
				heartbeat(value)
			}
		},
		"activity": func(m *nats.Msg) {
			var value domain.HostTelemetry
			if len(m.Data) <= domain.CommandPayloadLimit && json.Unmarshal(m.Data, &value) == nil && m.Subject == Subject(c.Cluster, "activity", value.HostID) {
				sample(value)
			}
		},
	}
	for kind, handler := range handlers {
		sub, err := c.nc.Subscribe(prefix+kind+".*", handler)
		if err != nil {
			return err
		}
		if err = sub.SetPendingLimits(domain.TelemetryPendingMessages, domain.TelemetryPendingBytes); err != nil {
			return err
		}
	}
	return c.nc.Flush()
}
